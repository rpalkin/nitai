//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apiv1 "ai-reviewer/gen/api/v1"
	"ai-reviewer/gen/api/v1/apiv1connect"
	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	tc "github.com/testcontainers/testcontainers-go/modules/compose"
)

// mrIIDCounter assigns unique MR IIDs to each test for isolation.
var mrIIDCounter atomic.Int64

// nextMRIID returns the next unique MR IID for test isolation.
func nextMRIID() string {
	return fmt.Sprintf("%d", mrIIDCounter.Add(1))
}

const e2eDBURL = "postgres://ai_reviewer:ai_reviewer@localhost:5432/ai_reviewer?sslmode=disable"

// DBReviewRun holds minimal review_run data for direct DB queries.
type DBReviewRun struct {
	ID                  string
	RepoID              string
	MRNumber            int64
	Status              string
	DiffHash            string
	RestateInvocationID *string
}

// QueryReviewRuns returns all review_runs for the given (repoID, mrNumber), ordered by created_at.
func QueryReviewRuns(t *testing.T, repoID string, mrNumber int64) []DBReviewRun {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), e2eDBURL)
	if err != nil {
		t.Fatalf("QueryReviewRuns: connect: %v", err)
	}
	defer conn.Close(context.Background())

	rows, err := conn.Query(context.Background(),
		`SELECT id, repo_id, mr_number, status, COALESCE(diff_hash, ''), restate_invocation_id
		 FROM review_runs
		 WHERE repo_id = $1 AND mr_number = $2
		 ORDER BY created_at`,
		repoID, mrNumber)
	if err != nil {
		t.Fatalf("QueryReviewRuns: query: %v", err)
	}
	defer rows.Close()

	var result []DBReviewRun
	for rows.Next() {
		var r DBReviewRun
		if err := rows.Scan(&r.ID, &r.RepoID, &r.MRNumber, &r.Status, &r.DiffHash, &r.RestateInvocationID); err != nil {
			t.Fatalf("QueryReviewRuns: scan: %v", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("QueryReviewRuns: rows: %v", err)
	}
	return result
}

// WaitForReviewRun polls the DB every 2s until a review_run with wantStatus exists for (repoID, mrNumber).
func WaitForReviewRun(t *testing.T, repoID string, mrNumber int64, wantStatus string, timeout time.Duration) *DBReviewRun {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		runs := QueryReviewRuns(t, repoID, mrNumber)
		for i := range runs {
			if runs[i].Status == wantStatus {
				return &runs[i]
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timeout waiting for review run with status=%s (repoID=%s, mr=%d)", wantStatus, repoID, mrNumber)
	return nil
}

// testingT is a minimal interface for TestMain (which doesn't get *testing.T).
type testingT interface {
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
}

// testMainT implements testingT for use in TestMain.
type testMainT struct{}

func (t *testMainT) Fatalf(format string, args ...any) {
	log.Fatalf(format, args...)
}
func (t *testMainT) Logf(format string, args ...any) {
	log.Printf(format, args...)
}

type TestClients struct {
	Provider    apiv1connect.ProviderServiceClient
	Repo        apiv1connect.RepoServiceClient
	Review      apiv1connect.ReviewServiceClient
	Auth        apiv1connect.AuthServiceClient
	Instruction apiv1connect.InstructionServiceClient
	Activity    apiv1connect.ActivityServiceClient
	BaseURL     string
	Token       string // JWT token for authenticated requests
}

func NewTestClients(baseURL string) *TestClients {
	httpClient := &http.Client{}
	return &TestClients{
		Provider:    apiv1connect.NewProviderServiceClient(httpClient, baseURL),
		Repo:        apiv1connect.NewRepoServiceClient(httpClient, baseURL),
		Review:      apiv1connect.NewReviewServiceClient(httpClient, baseURL),
		Auth:        apiv1connect.NewAuthServiceClient(httpClient, baseURL),
		Instruction: apiv1connect.NewInstructionServiceClient(httpClient, baseURL),
		Activity:    apiv1connect.NewActivityServiceClient(httpClient, baseURL),
		BaseURL:     baseURL,
	}
}

// NewAuthenticatedTestClients creates clients that inject the Authorization header on every request.
func NewAuthenticatedTestClients(baseURL, token string) *TestClients {
	httpClient := &http.Client{
		Transport: &authTransport{
			base:  http.DefaultTransport,
			token: token,
		},
	}
	return &TestClients{
		Provider:    apiv1connect.NewProviderServiceClient(httpClient, baseURL),
		Repo:        apiv1connect.NewRepoServiceClient(httpClient, baseURL),
		Review:      apiv1connect.NewReviewServiceClient(httpClient, baseURL),
		Auth:        apiv1connect.NewAuthServiceClient(httpClient, baseURL),
		Instruction: apiv1connect.NewInstructionServiceClient(httpClient, baseURL),
		Activity:    apiv1connect.NewActivityServiceClient(httpClient, baseURL),
		BaseURL:     baseURL,
		Token:       token,
	}
}

// authTransport is an http.RoundTripper that adds the Authorization header.
type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

// RegisterTestUser registers a new test user and returns the JWT token.
func RegisterTestUser(t testingT, baseURL string) string {
	client := apiv1connect.NewAuthServiceClient(&http.Client{}, baseURL)
	resp, err := client.Register(context.Background(),
		connect.NewRequest(&apiv1.RegisterRequest{
			Email:    "test@example.com",
			Password: "testpassword123",
		}))
	if err != nil {
		t.Fatalf("RegisterTestUser: %v", err)
	}
	return resp.Msg.Token
}

func PollReviewRun(
	t *testing.T,
	client apiv1connect.ReviewServiceClient,
	runID string,
	wantStatus apiv1.ReviewStatus,
	timeout time.Duration,
	interval time.Duration,
) *apiv1.ReviewRun {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.GetReviewRun(context.Background(),
			connect.NewRequest(&apiv1.GetReviewRunRequest{Id: runID}))
		if err != nil {
			t.Logf("GetReviewRun poll error (will retry): %v", err)
			time.Sleep(interval)
			continue
		}
		run := resp.Msg.ReviewRun
		t.Logf("poll: status=%s, comments=%d", run.Status, len(run.Comments))

		if run.Status == wantStatus {
			return run
		}
		// Fail fast on unexpected terminal status
		if run.Status == apiv1.ReviewStatus_REVIEW_STATUS_FAILED && wantStatus != apiv1.ReviewStatus_REVIEW_STATUS_FAILED {
			t.Fatalf("review run reached FAILED (expected %s)", wantStatus)
		}
		if run.Status == apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED && wantStatus != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
			t.Fatalf("review run reached COMPLETED (expected %s)", wantStatus)
		}
		time.Sleep(interval)
	}
	t.Fatalf("timeout waiting for review run %s to reach status %s", runID, wantStatus)
	return nil
}

func SendWebhook(t *testing.T, baseURL, providerID, webhookSecret string, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal webhook payload: %v", err)
	}

	req, err := http.NewRequest("POST",
		fmt.Sprintf("%s/webhooks/%s", baseURL, providerID),
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create webhook request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", webhookSecret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("send webhook: %v", err)
	}
	return resp
}

// SetupProviderAndRepo creates a provider pointing at the mock GitLab,
// finds the repo with remoteId="100", enables review, and returns all IDs.
// gitlabMock is used to get the host-reachable URL for the provider base URL.
func SetupProviderAndRepo(
	t *testing.T,
	clients *TestClients,
	gitlabMock *GitLabMock,
) (providerID, repoID, webhookSecret string) {
	t.Helper()

	t.Logf("CreateProvider: type=GITLAB_SELF_HOSTED, baseURL=%s", gitlabMock.HostURL())
	createResp, err := clients.Provider.CreateProvider(context.Background(),
		connect.NewRequest(&apiv1.CreateProviderRequest{
			Type:    apiv1.ProviderType_PROVIDER_TYPE_GITLAB_SELF_HOSTED,
			Name:    "test-provider",
			BaseUrl: gitlabMock.HostURL(),
			Token:   "test-token",
		}))
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	providerID = createResp.Msg.Provider.Id
	webhookSecret = createResp.Msg.WebhookSecret
	t.Logf("CreateProvider OK: providerID=%s", providerID)

	t.Log("waiting for repos to sync...")
	repos := waitForRepos(t, clients.Repo, providerID, 10*time.Second)
	t.Logf("found %d repos", len(repos))
	for _, repo := range repos {
		if repo.RemoteId == "100" {
			repoID = repo.Id
			break
		}
	}
	if repoID == "" {
		t.Fatal("repo with remoteId=100 not found")
	}
	t.Logf("found repo: repoID=%s (remoteId=100)", repoID)

	t.Log("EnableReview...")
	enableResp, err := clients.Repo.EnableReview(context.Background(),
		connect.NewRequest(&apiv1.EnableReviewRequest{
			RepoId: repoID,
		}))
	if err != nil {
		t.Fatalf("EnableReview: %v", err)
	}
	if !enableResp.Msg.Repository.ReviewEnabled {
		t.Fatal("EnableReview: reviewEnabled is false")
	}
	t.Log("EnableReview OK")

	return providerID, repoID, webhookSecret
}

// waitForHTTP polls a URL until it returns HTTP 200 or the timeout expires.
func waitForHTTP(t testingT, url string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Logf("ready: %s", url)
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for %s to return 200", url)
}

// waitForTCPReady polls a TCP address until a connection succeeds or the timeout expires.
// Useful for services like search-mcp that don't have an HTTP health endpoint.
func waitForTCPReady(t testingT, addr string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			conn.Close()
			t.Logf("ready: %s", addr)
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for %s to accept TCP connections", addr)
}

// waitForRestateServices polls the Restate admin API until all required services
// are registered, or the timeout expires.
func waitForRestateServices(t testingT, adminURL string, timeout time.Duration) {
	required := map[string]bool{
		"DiffFetcher": false,
		"PostReview":  false,
		"PRReview":    false,
		"Reviewer":    false,
		"RepoSyncer":  false,
		"Indexer":     false,
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(adminURL + "/services")
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		var result struct {
			Services []struct {
				Name string `json:"name"`
			} `json:"services"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			time.Sleep(time.Second)
			continue
		}
		resp.Body.Close()
		for _, svc := range result.Services {
			if _, ok := required[svc.Name]; ok {
				required[svc.Name] = true
			}
		}
		allReady := true
		for _, ready := range required {
			if !ready {
				allReady = false
				break
			}
		}
		if allReady {
			t.Logf("all Restate services registered")
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for Restate services to register")
}

// waitForRepos polls ListRepos until at least one repo appears or timeout is reached.
func waitForRepos(t *testing.T, client apiv1connect.RepoServiceClient, providerID string, timeout time.Duration) []*apiv1.Repository {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.ListRepos(context.Background(),
			connect.NewRequest(&apiv1.ListReposRequest{ProviderId: providerID}))
		if err == nil && len(resp.Msg.Repositories) > 0 {
			return resp.Msg.Repositories
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for repos to appear for provider %s", providerID)
	return nil
}

type E2EStack struct {
	Compose    tc.ComposeStack
	GitLab     *GitLabMock
	LLM        *LLMMock
	Clients    *TestClients
	createdEnv bool // true if we created ../.env and should remove it on teardown
}

func StartStack(t testingT, gitlabMock *GitLabMock, llmMock *LLMMock) *E2EStack {
	ctx := context.Background()

	// Extract mock server ports from their URLs
	llmPort := portFromURL(llmMock.Server.URL)

	// Compose files (relative to e2e/ directory — tests run from e2e/)
	stack, err := tc.NewDockerComposeWith(
		tc.StackIdentifier("e2e"),
		tc.WithStackFiles("../docker-compose.yml", "docker-compose.e2e.yml"),
	)
	if err != nil {
		t.Fatalf("creating compose stack: %v", err)
	}

	// Generate a random encryption key (32 bytes = 64 hex chars)
	encryptionKey := generateHexKey(32)
	// Generate a JWT secret (32 bytes = 64 hex chars)
	jwtSecret := generateHexKey(32)

	// Write temporary .env file (docker-compose.yml uses env_file: .env)
	createdEnv := writeEnvFile(t, encryptionKey, jwtSecret)

	// tc.Wait(true) passes --wait to docker compose, which treats any exited container
	// (including one-shot init containers like restate-register) as a failure.
	// Use Up without Wait and poll for readiness manually instead.
	err = stack.
		WithEnv(map[string]string{
			"OPENROUTER_API_KEY":  "test-key-not-used",
			"OPENROUTER_BASE_URL": fmt.Sprintf("http://host.docker.internal:%s/v1", llmPort),
			"ENCRYPTION_KEY":      encryptionKey,
			"JWT_SECRET":          jwtSecret,
			"REVIEW_MODEL":        "test-model",
			"MAX_TOKENS":          "4096",
			"EMBEDDING_MODEL":     "text-embedding-3-small",
		}).
		Up(ctx)

	if err != nil {
		t.Fatalf("starting compose stack: %v", err)
	}

	// Poll for api-server, Restate, and search-mcp readiness, then wait for service registration.
	waitForHTTP(t, "http://localhost:8090/healthz", 60*time.Second)
	waitForHTTP(t, "http://localhost:9070/health", 60*time.Second)
	waitForTCPReady(t, "localhost:8081", 60*time.Second)
	waitForRestateServices(t, "http://localhost:9070", 120*time.Second)

	// Register a test user and create authenticated clients
	token := RegisterTestUser(t, "http://localhost:8090")
	authenticatedClients := NewAuthenticatedTestClients("http://localhost:8090", token)

	return &E2EStack{
		Compose:    stack,
		GitLab:     gitlabMock,
		LLM:        llmMock,
		Clients:    authenticatedClients,
		createdEnv: createdEnv,
	}
}

func StopStack(t testingT, stack *E2EStack) {
	ctx := context.Background()
	if os.Getenv("E2E_KEEP_STACK") == "1" {
		t.Logf("E2E_KEEP_STACK=1, skipping teardown")
		return
	}
	if err := stack.Compose.Down(ctx, tc.RemoveVolumes(true), tc.RemoveOrphans(true)); err != nil {
		t.Logf("compose down error: %v", err)
	}
	if stack.createdEnv {
		if err := os.Remove("../.env"); err != nil {
			t.Logf("removing generated .env: %v", err)
		}
	}
}

// writeEnvFile creates a .env file in the repo root with required vars.
// Returns true if a new file was created (vs. an existing one being skipped).
func writeEnvFile(t testingT, encryptionKey, jwtSecret string) bool {
	envPath := "../.env"
	// Don't overwrite existing .env
	if _, err := os.Stat(envPath); err == nil {
		t.Logf("using existing .env file — ensure it has ENCRYPTION_KEY, JWT_SECRET, REVIEW_MODEL, EMBEDDING_MODEL set")
		return false
	}
	content := fmt.Sprintf(`OPENROUTER_API_KEY=test-key-not-used
ENCRYPTION_KEY=%s
JWT_SECRET=%s
REVIEW_MODEL=test-model
MAX_TOKENS=4096
EMBEDDING_MODEL=text-embedding-3-small
`, encryptionKey, jwtSecret)
	if err := os.WriteFile(envPath, []byte(content), 0644); err != nil {
		t.Fatalf("writing .env file: %v", err)
	}
	return true
}

func portFromURL(rawURL string) string {
	u, _ := url.Parse(rawURL)
	_, port, _ := net.SplitHostPort(u.Host)
	return port
}

func generateHexKey(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TestContext encapsulates per-test isolated state for parallel test execution.
type TestContext struct {
	T             *testing.T
	MRIID         string // unique MR IID from atomic counter
	Marker        string // "e2e-marker-<MRIID>" embedded in MR titles
	ProviderID    string
	RepoID        string
	WebhookSecret string
	ProjectID     string // typically "100"
}

// NewTestContext allocates a unique MR IID, creates provider+repo, and registers the marker.
// The marker is embedded in the MR title so it flows through to the LLM request body.
func NewTestContext(t *testing.T) *TestContext {
	t.Helper()

	// Allocate unique MR IID and marker
	mrIID := nextMRIID()
	marker := fmt.Sprintf("e2e-marker-%s", mrIID)

	// Create provider and repo
	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	tc := &TestContext{
		T:             t,
		MRIID:         mrIID,
		Marker:        marker,
		ProviderID:    providerID,
		RepoID:        repoID,
		WebhookSecret: webhookSecret,
		ProjectID:     "100",
	}

	// Register marker with LLM mock for request tracking
	llm.RegisterMarker(marker)

	// Cleanup function to unregister marker and reset MR-specific state
	t.Cleanup(func() {
		llm.UnregisterMarker(marker)
		gitlab.ResetForMR(tc.ProjectID, tc.MRIID)
	})

	return tc
}

// SetMR configures the mock GitLab for this test's MR with the marker embedded in the title.
// The marker is added to the MR title so it flows through to LLM requests.
func (tc *TestContext) SetMR(cfg *MRConfig) {
	tc.T.Helper()

	// Inject marker into the MR title in the Details JSON
	if cfg.Details != nil {
		var details map[string]any
		if err := json.Unmarshal(cfg.Details, &details); err == nil {
			if title, ok := details["title"].(string); ok {
				details["title"] = fmt.Sprintf("%s %s", title, tc.Marker)
			} else {
				details["title"] = tc.Marker
			}
			if updated, err := json.Marshal(details); err == nil {
				cfg.Details = updated
			}
		}
	}

	gitlab.SetMR(tc.ProjectID, tc.MRIID, cfg)
}

// Notes returns only notes posted to this test's MR.
func (tc *TestContext) Notes() []PostedNote {
	return gitlab.NotesFor(tc.ProjectID, tc.MRIID)
}

// Discussions returns only discussions posted to this test's MR.
func (tc *TestContext) Discussions() []PostedDiscussion {
	return gitlab.DiscussionsFor(tc.ProjectID, tc.MRIID)
}

// LLMRequestCount returns the count of LLM requests containing this test's marker.
func (tc *TestContext) LLMRequestCount() int {
	return llm.RequestCountForMarker(tc.Marker)
}

// LLMRequests returns LLM requests whose body contains this test's marker.
func (tc *TestContext) LLMRequests() []LLMRequest {
	return llm.RequestsForMarker(tc.Marker)
}

// SetResponseFunc sets a custom LLM response for this test's requests.
func (tc *TestContext) SetResponseFunc(fn func(reqBody []byte) (int, json.RawMessage)) {
	llm.RegisterResponseFuncForMarker(tc.Marker, fn)
}

// SetEmbeddingResponseFunc sets a custom embedding response for this test's requests.
func (tc *TestContext) SetEmbeddingResponseFunc(fn func(reqBody []byte) (int, json.RawMessage)) {
	llm.RegisterEmbeddingResponseFuncForMarker(tc.Marker, fn)
}

// SendWebhook sends a webhook for this test's provider/MR.
func (tc *TestContext) SendWebhook(payload map[string]any) *http.Response {
	tc.T.Helper()

	// Parse MR IID and project ID as integers (webhook handler expects int64)
	mrNum, _ := parseInt64(tc.MRIID)
	projNum, _ := parseInt64(tc.ProjectID)

	// Set project ID and MR IID in the payload
	if objAttrs, ok := payload["object_attributes"].(map[string]any); ok {
		objAttrs["iid"] = mrNum
	}
	// Ensure project field exists and set the project ID
	if project, ok := payload["project"].(map[string]any); ok {
		project["id"] = projNum
	} else {
		payload["project"] = map[string]any{"id": projNum}
	}

	return SendWebhook(tc.T, clients.BaseURL, tc.ProviderID, tc.WebhookSecret, payload)
}

// TriggerReview triggers a review for this test's repo/MR.
func (tc *TestContext) TriggerReview() (string, error) {
	mrNum, _ := parseInt64(tc.MRIID)

	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{
			RepoId:   tc.RepoID,
			MrNumber: mrNum,
		}))
	if err != nil {
		return "", err
	}
	return triggerResp.Msg.ReviewRun.Id, nil
}

// WaitForReviewRun waits for a review run with the given status for this test's MR.
func (tc *TestContext) WaitForReviewRun(status string, timeout time.Duration) *DBReviewRun {
	mrNum, _ := parseInt64(tc.MRIID)
	return WaitForReviewRun(tc.T, tc.RepoID, mrNum, status, timeout)
}

// QueryReviewRuns returns all review runs for this test's MR.
func (tc *TestContext) QueryReviewRuns() []DBReviewRun {
	mrNum, _ := parseInt64(tc.MRIID)
	return QueryReviewRuns(tc.T, tc.RepoID, mrNum)
}

// PollReviewRun polls for a review run with the given status for this test's MR.
func (tc *TestContext) PollReviewRun(runID string, wantStatus apiv1.ReviewStatus, timeout, interval time.Duration) *apiv1.ReviewRun {
	return PollReviewRun(tc.T, clients.Review, runID, wantStatus, timeout, interval)
}

// parseInt64 parses a string to int64.
func parseInt64(s string) (int64, error) {
	var result int64
	_, err := fmt.Sscanf(s, "%d", &result)
	return result, err
}

// MRBranchResult contains real git data for configuring a mock MR.
type MRBranchResult struct {
	BaseSHA      string          // commit SHA on target branch (before changes)
	HeadSHA      string          // commit SHA on feature branch (after changes)
	SourceBranch string          // feature branch name
	TargetBranch string          // base branch name (e.g. "main")
	Changes      json.RawMessage // GitLab-format changes JSON (computed via git diff)
	Versions     json.RawMessage // GitLab-format versions JSON
}

// CreateMRBranch creates a feature branch with the given file changes on the shared bare repo.
// It commits the files, computes the diff against the base branch, and returns all data
// needed to configure a realistic MR in the mock GitLab.
//
// Parameters:
//   - t: testing T interface (supports both *testing.T and testingT)
//   - barePath: path to the bare git repo
//   - branchName: name for the feature branch (should be unique per test)
//   - baseBranch: target branch to diff against (usually "main")
//   - files: map of file path -> content to commit on the feature branch
//
// Returns MRBranchResult with real SHAs and computed diff.
func CreateMRBranch(t testingT, barePath, branchName, baseBranch string, files map[string]string) *MRBranchResult {
	// Check branch uniqueness
	checkCmd := exec.Command("git", "--git-dir="+barePath, "rev-parse", "--verify", "refs/heads/"+branchName)
	if err := checkCmd.Run(); err == nil {
		t.Fatalf("CreateMRBranch: branch %q already exists in bare repo", branchName)
	}

	// Get base branch HEAD SHA
	baseCmd := exec.Command("git", "--git-dir="+barePath, "rev-parse", "refs/heads/"+baseBranch)
	baseOut, err := baseCmd.Output()
	if err != nil {
		t.Fatalf("CreateMRBranch: get base SHA: %v", err)
	}
	baseSHA := strings.TrimSpace(string(baseOut))

	// Clone to temp working directory
	workDir, err := os.MkdirTemp("", "e2e-mr-branch-*")
	if err != nil {
		t.Fatalf("CreateMRBranch: temp dir: %v", err)
	}
	defer os.RemoveAll(workDir)

	cloneCmd := exec.Command("git", "clone", barePath, workDir)
	cloneCmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=E2E Test",
		"GIT_AUTHOR_EMAIL=e2e@test.com",
		"GIT_COMMITTER_NAME=E2E Test",
		"GIT_COMMITTER_EMAIL=e2e@test.com",
	)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("CreateMRBranch: clone: %v\n%s", err, out)
	}

	// Create and checkout branch from base
	checkoutCmd := exec.Command("git", "checkout", "-b", branchName, "origin/"+baseBranch)
	checkoutCmd.Dir = workDir
	checkoutCmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=E2E Test",
		"GIT_AUTHOR_EMAIL=e2e@test.com",
		"GIT_COMMITTER_NAME=E2E Test",
		"GIT_COMMITTER_EMAIL=e2e@test.com",
	)
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		t.Fatalf("CreateMRBranch: checkout -b %s: %v\n%s", branchName, err, out)
	}

	// Write files
	for filePath, content := range files {
		absPath := filepath.Join(workDir, filePath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			t.Fatalf("CreateMRBranch: mkdir %s: %v", filepath.Dir(absPath), err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			t.Fatalf("CreateMRBranch: write %s: %v", filePath, err)
		}
	}

	// Add and commit
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=E2E Test",
			"GIT_AUTHOR_EMAIL=e2e@test.com",
			"GIT_COMMITTER_NAME=E2E Test",
			"GIT_COMMITTER_EMAIL=e2e@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("CreateMRBranch: git %v: %v\n%s", args, err, out)
		}
	}
	runGit("add", ".")
	runGit("commit", "-m", "E2E test commit for "+branchName)

	// Push to bare
	pushCmd := exec.Command("git", "push", "origin", branchName)
	pushCmd.Dir = workDir
	pushCmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=E2E Test",
		"GIT_AUTHOR_EMAIL=e2e@test.com",
		"GIT_COMMITTER_NAME=E2E Test",
		"GIT_COMMITTER_EMAIL=e2e@test.com",
	)
	if out, err := pushCmd.CombinedOutput(); err != nil {
		t.Fatalf("CreateMRBranch: push: %v\n%s", err, out)
	}

	// Get HEAD SHA
	revCmd := exec.Command("git", "rev-parse", "HEAD")
	revCmd.Dir = workDir
	revOut, err := revCmd.Output()
	if err != nil {
		t.Fatalf("CreateMRBranch: rev-parse HEAD: %v", err)
	}
	headSHA := strings.TrimSpace(string(revOut))

	// Compute diff
	diffCmd := exec.Command("git", "diff", baseSHA+".."+headSHA)
	diffCmd.Dir = workDir
	diffOut, err := diffCmd.Output()
	if err != nil {
		t.Fatalf("CreateMRBranch: diff: %v", err)
	}

	// Update server-info on bare repo
	updateCmd := exec.Command("git", "--git-dir="+barePath, "update-server-info")
	if out, err := updateCmd.CombinedOutput(); err != nil {
		t.Logf("warning: update-server-info: %v\n%s", err, out)
	}

	// Parse diff into GitLab changes format
	changesJSON := parseUnifiedDiffToGitLabChanges(string(diffOut))

	// Build versions JSON
	versionsJSON, err := json.Marshal([]map[string]any{
		{
			"id":               1,
			"head_commit_sha":  headSHA,
			"base_commit_sha":  baseSHA,
			"start_commit_sha": baseSHA,
		},
	})
	if err != nil {
		t.Fatalf("CreateMRBranch: marshal versions: %v", err)
	}

	t.Logf("CreateMRBranch: created %s at %s (base=%s)", branchName, headSHA[:8], baseSHA[:8])

	return &MRBranchResult{
		BaseSHA:      baseSHA,
		HeadSHA:      headSHA,
		SourceBranch: branchName,
		TargetBranch: baseBranch,
		Changes:      changesJSON,
		Versions:     json.RawMessage(versionsJSON),
	}
}

// parseUnifiedDiffToGitLabChanges converts a unified diff output into GitLab's changes JSON format.
func parseUnifiedDiffToGitLabChanges(diffOutput string) json.RawMessage {
	var changes []map[string]any

	// Split on "diff --git" headers
	sections := strings.Split(diffOutput, "diff --git ")
	for _, section := range sections {
		if strings.TrimSpace(section) == "" {
			continue
		}

		// Parse the diff header: a/X b/Y
		lines := strings.SplitN(section, "\n", 2)
		if len(lines) < 2 {
			continue
		}
		header := strings.TrimSpace(lines[0])

		// Extract old_path and new_path from "a/X b/Y" or "a/X b/X" (same path)
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 {
			continue
		}
		oldPath := strings.TrimPrefix(parts[0], "a/")
		newPath := strings.TrimPrefix(parts[1], "b/")

		// Find the diff content (from first @@ onwards)
		rest := lines[1]
		atIndex := strings.Index(rest, "@@")
		var diffContent string
		if atIndex >= 0 {
			// Include all @@ sections for this file
			diffContent = rest[atIndex:]
		}

		// Detect file status
		newFile := strings.Contains(rest, "--- /dev/null")
		deletedFile := strings.Contains(rest, "+++ /dev/null")
		renamedFile := oldPath != newPath && !newFile && !deletedFile

		change := map[string]any{
			"old_path":     oldPath,
			"new_path":     newPath,
			"diff":         diffContent,
			"new_file":     newFile,
			"deleted_file": deletedFile,
			"renamed_file": renamedFile,
		}
		changes = append(changes, change)
	}

	result := map[string]any{"changes": changes}
	resultJSON, _ := json.Marshal(result)
	return json.RawMessage(resultJSON)
}

// SetMRFromBranch configures the mock GitLab MR using real git data from an MRBranchResult.
// Builds Details, Changes, and Versions JSON, then delegates to tc.SetMR().
// The marker is injected into the title automatically by SetMR.
func (tc *TestContext) SetMRFromBranch(result *MRBranchResult, title, description, author string) {
	tc.T.Helper()

	details := map[string]any{
		"title":         title,
		"description":   description,
		"author":        map[string]string{"username": author},
		"source_branch": result.SourceBranch,
		"target_branch": result.TargetBranch,
		"sha":           result.HeadSHA,
		"draft":         false,
	}
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		tc.T.Fatalf("SetMRFromBranch: marshal details: %v", err)
	}

	tc.SetMR(&MRConfig{
		Details:  json.RawMessage(detailsJSON),
		Changes:  result.Changes,
		Versions: result.Versions,
	})
}

// CommitFileToBareRepo commits a file to the bare git repo on a specified branch.
// It creates the branch if it doesn't exist. Returns the commit SHA.
// This is used by rules tests to add .review-rules.yaml files.
func CommitFileToBareRepo(t *testing.T, barePath, branch, filePath string, content []byte) string {
	t.Helper()

	// Use git commands to add a commit to the bare repo
	// 1. Clone bare to a temp working directory
	// 2. Create/update file
	// 3. Commit on the branch
	// 4. Push back to bare
	// 5. Return SHA

	workDir, err := os.MkdirTemp("", "e2e-commit-work-*")
	if err != nil {
		t.Fatalf("CommitFileToBareRepo: temp dir: %v", err)
	}
	defer os.RemoveAll(workDir)

	// Clone from bare to working directory
	cloneCmd := exec.Command("git", "clone", "--branch", branch, barePath, workDir)
	cloneCmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if err := cloneCmd.Run(); err != nil {
		// Branch might not exist - try cloning without branch specification and create it
		cloneCmd = exec.Command("git", "clone", barePath, workDir)
		cloneCmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cloneCmd.CombinedOutput(); err != nil {
			t.Fatalf("git clone: %v\n%s", err, out)
		}

		// Create and checkout the branch
		checkoutCmd := exec.Command("git", "checkout", "-b", branch)
		checkoutCmd.Dir = workDir
		if out, err := checkoutCmd.CombinedOutput(); err != nil {
			t.Fatalf("git checkout -b %s: %v\n%s", branch, err, out)
		}
	}

	// Write the file
	absPath := filepath.Join(workDir, filePath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(absPath, content, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Add and commit
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit("add", filePath)
	runGit("commit", "-m", "add "+filePath)

	// For existing branches (e.g. "main"), pull-rebase before push to handle
	// concurrent pushes from parallel tests.
	pullCmd := exec.Command("git", "pull", "--rebase", "origin", branch)
	pullCmd.Dir = workDir
	pullCmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	_ = pullCmd.Run() // ignore error for new branches with no upstream

	runGit("push", "origin", branch)

	// Get the SHA
	revParseCmd := exec.Command("git", "rev-parse", "HEAD")
	revParseCmd.Dir = workDir
	shaBytes, err := revParseCmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	sha := strings.TrimSpace(string(shaBytes))

	// Update the bare repo's server-info
	updateCmd := exec.Command("git", "--git-dir="+barePath, "update-server-info")
	if out, err := updateCmd.CombinedOutput(); err != nil {
		t.Logf("warning: update-server-info: %v\n%s", err, out)
	}

	t.Logf("CommitFileToBareRepo: committed %s on branch %s at SHA %s", filePath, branch, sha)
	return sha
}
