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
	"testing"
	"time"

	apiv1 "ai-reviewer/gen/api/v1"
	"ai-reviewer/gen/api/v1/apiv1connect"
	"connectrpc.com/connect"
	tc "github.com/testcontainers/testcontainers-go/modules/compose"
)

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
	BaseURL  string
}

func NewTestClients(baseURL string) *TestClients {
	httpClient := &http.Client{}
	return &TestClients{
		Provider: apiv1connect.NewProviderServiceClient(httpClient, baseURL),
		Repo:     apiv1connect.NewRepoServiceClient(httpClient, baseURL),
		Review:   apiv1connect.NewReviewServiceClient(httpClient, baseURL),
		BaseURL:  baseURL,
	}
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

// waitForRestateServices polls the Restate admin API until DiffFetcher, PostReview,
// PRReview, and Reviewer are all registered, or the timeout expires.
func waitForRestateServices(t testingT, adminURL string, timeout time.Duration) {
	required := map[string]bool{
		"DiffFetcher": false,
		"PostReview":  false,
		"PRReview":    false,
		"Reviewer":    false,
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
	Compose      tc.ComposeStack
	GitLab       *GitLabMock
	LLM          *LLMMock
	Clients      *TestClients
	createdEnv   bool // true if we created ../.env and should remove it on teardown
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

	// Write temporary .env file (docker-compose.yml uses env_file: .env)
	createdEnv := writeEnvFile(t, encryptionKey)

	// tc.Wait(true) passes --wait to docker compose, which treats any exited container
	// (including one-shot init containers like restate-register) as a failure.
	// Use Up without Wait and poll for readiness manually instead.
	err = stack.
		WithEnv(map[string]string{
			"OPENROUTER_API_KEY":  "test-key-not-used",
			"OPENROUTER_BASE_URL": fmt.Sprintf("http://host.docker.internal:%s/v1", llmPort),
			"ENCRYPTION_KEY":      encryptionKey,
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

	clients := NewTestClients("http://localhost:8090")

	return &E2EStack{
		Compose:    stack,
		GitLab:     gitlabMock,
		LLM:        llmMock,
		Clients:    clients,
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
func writeEnvFile(t testingT, encryptionKey string) bool {
	envPath := "../.env"
	// Don't overwrite existing .env
	if _, err := os.Stat(envPath); err == nil {
		t.Logf("using existing .env file — ensure it has ENCRYPTION_KEY, REVIEW_MODEL, EMBEDDING_MODEL set")
		return false
	}
	content := fmt.Sprintf(`OPENROUTER_API_KEY=test-key-not-used
ENCRYPTION_KEY=%s
REVIEW_MODEL=test-model
MAX_TOKENS=4096
EMBEDDING_MODEL=text-embedding-3-small
`, encryptionKey)
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
