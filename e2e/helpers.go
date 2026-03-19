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
	Provider apiv1connect.ProviderServiceClient
	Repo     apiv1connect.RepoServiceClient
	Review   apiv1connect.ReviewServiceClient
	Auth     apiv1connect.AuthServiceClient
	BaseURL  string
	Token    string // JWT token for authenticated requests
}

func NewTestClients(baseURL string) *TestClients {
	httpClient := &http.Client{}
	return &TestClients{
		Provider: apiv1connect.NewProviderServiceClient(httpClient, baseURL),
		Repo:     apiv1connect.NewRepoServiceClient(httpClient, baseURL),
		Review:   apiv1connect.NewReviewServiceClient(httpClient, baseURL),
		Auth:     apiv1connect.NewAuthServiceClient(httpClient, baseURL),
		BaseURL:  baseURL,
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
		Provider: apiv1connect.NewProviderServiceClient(httpClient, baseURL),
		Repo:     apiv1connect.NewRepoServiceClient(httpClient, baseURL),
		Review:   apiv1connect.NewReviewServiceClient(httpClient, baseURL),
		Auth:     apiv1connect.NewAuthServiceClient(httpClient, baseURL),
		BaseURL:  baseURL,
		Token:    token,
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

	// Poll for api-server and Restate readiness, then wait for service registration.
	waitForHTTP(t, "http://localhost:8090/healthz", 60*time.Second)
	waitForHTTP(t, "http://localhost:9070/health", 60*time.Second)
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
