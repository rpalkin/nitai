//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apiv1 "ai-reviewer/gen/api/v1"
	"connectrpc.com/connect"
)

var (
	stack   *E2EStack
	clients *TestClients
	gitlab  *GitLabMock
	llm     *LLMMock
)

func TestMain(m *testing.M) {
	// 1. Start mock servers FIRST (before Docker containers need them)
	gitlab = NewGitLabMock()
	llm = NewLLMMock()

	// 2. Configure mock GitLab with default project list
	gitlab.SetProjects([]GitLabProject{
		{ID: 100, Name: "test-project", PathWithNamespace: "group/test-project", HTTPURLToRepo: "http://gitlab.example.com/group/test-project.git", DefaultBranch: "main"},
	})

	// 3. Create a bare git repo served via dumb HTTP for RepoSyncer tests.
	//    The repo has test files with known content used by tool integration tests.
	t0 := &testMainT{}
	gitRepoDir := setupBareGitRepo(t0)
	defer os.RemoveAll(gitRepoDir)
	gitlab.SetGitRepoPath(gitRepoDir)

	// 4. Start Docker Compose stack
	stack = StartStack(t0, gitlab, llm)
	clients = stack.Clients

	// 5. Run tests
	code := m.Run()

	// 6. Teardown (skip if tests failed to allow debugging)
	if code != 0 {
		t0.Logf("Tests failed with exit code %d. Keeping stack running for debugging.", code)
		t0.Logf("To clean up manually: docker compose -p e2e down -v")
	} else {
		StopStack(t0, stack)
		gitlab.Server.Close()
		llm.Server.Close()
	}

	os.Exit(code)
}

// setupBareGitRepo creates a temporary bare git repository with test files.
// Returns the path to the bare repo directory (caller must os.RemoveAll it).
func setupBareGitRepo(t testingT) string {
	// Create a working dir to build the repo, then clone as bare
	workDir, err := os.MkdirTemp("", "e2e-git-work-*")
	if err != nil {
		t.Fatalf("MkdirTemp (work): %v", err)
	}
	defer os.RemoveAll(workDir)

	bareDir, err := os.MkdirTemp("", "e2e-git-bare-*")
	if err != nil {
		t.Fatalf("MkdirTemp (bare): %v", err)
	}

	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Init working repo
	run(workDir, "init", "-b", "main")
	run(workDir, "config", "user.email", "test@example.com")
	run(workDir, "config", "user.name", "Test")

	// Create test files
	files := map[string]string{
		"src/handler.go": `package handler

import "fmt"

func ProcessOrder(order *Order) error {
	result := CalculateTotal(order.Items)
	if result == nil {
		return nil
	}
	fmt.Println(result)
	return nil
}
`,
		"src/util.go": `package handler

// CalculateTotal sums all item prices.
// func CalculateTotal(items []Item) *int
func CalculateTotal(items []Item) *int {
	total := 0
	for _, item := range items {
		total += item.Price
	}
	return &total
}
`,
		"pkg/mathutil/mathutil.go": `package mathutil

// Foo adds two integers.
func Foo(x, y int) int {
	return x + y
}
`,
	}

	for relPath, content := range files {
		absPath := filepath.Join(workDir, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			t.Fatalf("MkdirAll %s: %v", filepath.Dir(absPath), err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile %s: %v", relPath, err)
		}
	}

	run(workDir, "add", ".")
	run(workDir, "commit", "-m", "initial commit")

	// Clone as bare into bareDir (overwrites the temp dir contents)
	os.RemoveAll(bareDir)
	if err := exec.Command("git", "clone", "--bare", workDir, bareDir).Run(); err != nil {
		t.Fatalf("git clone --bare: %v", err)
	}

	// Enable dumb HTTP protocol
	if err := exec.Command("git", "--git-dir="+bareDir, "update-server-info").Run(); err != nil {
		t.Fatalf("git update-server-info: %v", err)
	}

	t.Logf("bare git repo created at %s", bareDir)
	return bareDir
}

func TestFullPipelineViaTriggerReview(t *testing.T) {
	t.Log("--- Setup: configuring mock GitLab for MR iid=1 ---")
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1,
            "title": "Add order processing",
            "description": "Implements order handler",
            "author": {"username": "alice"},
            "source_branch": "feature/orders",
            "target_branch": "main",
            "sha": "bbb222",
            "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{
                "old_path": "src/handler.go",
                "new_path": "src/handler.go",
                "diff": "@@ -10,6 +10,12 @@ package handler\n import \"fmt\"\n \n+func ProcessOrder(order *Order) error {\n+    result := CalculateTotal(order.Items)\n+    if result == nil {\n+        return nil\n+    }\n+    fmt.Println(result)\n+    return nil\n+}",
                "new_file": false, "deleted_file": false, "renamed_file": false
            }]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1,
            "head_commit_sha": "bbb222",
            "base_commit_sha": "aaa111",
            "start_commit_sha": "aaa111"
        }]`),
	})

	t.Log("configuring mock LLM default response")
	llm.DefaultResponse = defaultLLMResponse

	// Clean up recorded requests after each test to prevent leakage between tests
	t.Cleanup(func() {
		gitlab.Reset()
		llm.Reset()
	})

	// Reset any requests accumulated during setup before the actual test
	gitlab.Reset()
	llm.Reset()

	t.Log("--- Step 1-3: Create provider, find repo, enable review ---")
	providerID, repoID, _ := SetupProviderAndRepo(t, clients, gitlab)
	_ = providerID

	t.Logf("--- Step 4: TriggerReview (repoID=%s, MR=1) ---", repoID)
	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{
			RepoId:   repoID,
			MrNumber: 1,
		}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id
	t.Logf("TriggerReview OK: runID=%s, status=%s", runID, triggerResp.Msg.ReviewRun.Status)
	if triggerResp.Msg.ReviewRun.Status != apiv1.ReviewStatus_REVIEW_STATUS_PENDING {
		t.Fatalf("expected PENDING, got %s", triggerResp.Msg.ReviewRun.Status)
	}

	t.Log("--- Step 5: Polling until COMPLETED ---")
	// After 2.9: pipeline includes SyncRepo + IndexRepo; allow extra time.
	run := PollReviewRun(t, clients.Review, runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED,
		90*time.Second, 2*time.Second)

	t.Log("--- Assertions ---")

	// A1: Status
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}

	// A2: Comment count
	t.Logf("A2: review run has %d comments", len(run.Comments))
	if len(run.Comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(run.Comments))
	}

	// A3: Comment 0 content
	t.Logf("A3: comment[0] file=%s line=%d", run.Comments[0].FilePath, run.Comments[0].LineStart)
	c0 := run.Comments[0]
	if c0.FilePath != "src/handler.go" {
		t.Errorf("comment[0].filePath = %q, want %q", c0.FilePath, "src/handler.go")
	}
	if c0.LineStart != 12 {
		t.Errorf("comment[0].lineStart = %d, want 12", c0.LineStart)
	}
	if !strings.Contains(c0.Body, "CalculateTotal") {
		t.Errorf("comment[0].body missing 'CalculateTotal': %s", c0.Body)
	}

	// A4: Comment 1 content
	t.Logf("A4: comment[1] file=%s line=%d", run.Comments[1].FilePath, run.Comments[1].LineStart)
	c1 := run.Comments[1]
	if c1.FilePath != "src/handler.go" {
		t.Errorf("comment[1].filePath = %q, want %q", c1.FilePath, "src/handler.go")
	}
	if c1.LineStart != 17 {
		t.Errorf("comment[1].lineStart = %d, want 17", c1.LineStart)
	}
	if !strings.Contains(c1.Body, "swallows the result") {
		t.Errorf("comment[1].body missing 'swallows the result': %s", c1.Body)
	}

	t.Log("A5-A7: checking GitLab GET requests")
	mrGETs := gitlab.RequestsTo("GET", "/api/v4/projects/100/merge_requests/1")
	var mrDetailGETs, changesGETs, versionsGETs []RecordedRequest
	for _, r := range mrGETs {
		switch {
		case strings.HasSuffix(r.Path, "/changes"):
			changesGETs = append(changesGETs, r)
		case strings.HasSuffix(r.Path, "/versions"):
			versionsGETs = append(versionsGETs, r)
		default:
			mrDetailGETs = append(mrDetailGETs, r)
		}
	}
	t.Logf("  MR detail GETs: %d, changes GETs: %d, versions GETs: %d", len(mrDetailGETs), len(changesGETs), len(versionsGETs))
	if len(mrDetailGETs) != 1 {
		t.Errorf("expected 1 GET /merge_requests/1, got %d", len(mrDetailGETs))
	}
	if len(changesGETs) != 1 {
		t.Errorf("expected 1 GET .../changes, got %d", len(changesGETs))
	}
	// getMRVersions is called once per inline comment (no caching), so expect 2 calls for 2 comments
	if len(versionsGETs) != 2 {
		t.Errorf("expected 2 GET .../versions, got %d", len(versionsGETs))
	}

	// A8: Summary note posted
	notes := gitlab.Notes()
	t.Logf("A8: %d summary notes posted", len(notes))
	if len(notes) != 1 {
		t.Fatalf("expected 1 posted note, got %d", len(notes))
	}
	if !strings.Contains(notes[0].Body, "nil pointer") {
		t.Errorf("summary note missing 'nil pointer': %s", notes[0].Body)
	}

	// A9: Inline discussions posted
	discussions := gitlab.Discussions()
	t.Logf("A9: %d inline discussions posted", len(discussions))
	if len(discussions) != 2 {
		t.Fatalf("expected 2 posted discussions, got %d", len(discussions))
	}

	// Discussion 0
	d0 := discussions[0]
	if d0.Position.NewPath != "src/handler.go" {
		t.Errorf("disc[0] new_path = %q, want %q", d0.Position.NewPath, "src/handler.go")
	}
	if d0.Position.NewLine != 12 {
		t.Errorf("disc[0] new_line = %d, want 12", d0.Position.NewLine)
	}
	if !strings.Contains(d0.Body, "CalculateTotal") {
		t.Errorf("disc[0] body missing 'CalculateTotal': %s", d0.Body)
	}

	// Discussion 1
	d1 := discussions[1]
	if d1.Position.NewPath != "src/handler.go" {
		t.Errorf("disc[1] new_path = %q, want %q", d1.Position.NewPath, "src/handler.go")
	}
	if d1.Position.NewLine != 17 {
		t.Errorf("disc[1] new_line = %d, want 17", d1.Position.NewLine)
	}

	t.Log("A10: checking SHA values in discussion positions")
	for i, d := range discussions {
		if d.Position.BaseSHA != "aaa111" {
			t.Errorf("disc[%d] base_sha = %q, want %q", i, d.Position.BaseSHA, "aaa111")
		}
		if d.Position.HeadSHA != "bbb222" {
			t.Errorf("disc[%d] head_sha = %q, want %q", i, d.Position.HeadSHA, "bbb222")
		}
		if d.Position.StartSHA != "aaa111" {
			t.Errorf("disc[%d] start_sha = %q, want %q", i, d.Position.StartSHA, "aaa111")
		}
	}

	// A11: LLM request count
	t.Logf("A11: %d LLM requests made", llm.RequestCount())
	if llm.RequestCount() != 1 {
		t.Errorf("expected 1 LLM request, got %d", llm.RequestCount())
	}

	t.Log("A12-A14: checking LLM request content")
	llmReqs := llm.Requests()
	if len(llmReqs) > 0 {
		body := string(llmReqs[0].Body)
		if !strings.Contains(body, "final_result") {
			t.Errorf("LLM request missing 'final_result' tool")
		}
		if !strings.Contains(body, "ProcessOrder") {
			t.Errorf("LLM request missing diff content 'ProcessOrder'")
		}
		if !strings.Contains(body, "read_file") {
			t.Errorf("LLM request missing 'read_file' tool (phase 2.7)")
		}
		if !strings.Contains(body, "search_codebase") {
			t.Errorf("LLM request missing 'search_codebase' tool (phase 2.8)")
		}
	}
}

// TestFullPipelineViaWebhook verifies the complete happy path triggered by a GitLab webhook.
func TestFullPipelineViaWebhook(t *testing.T) {
	t.Log("--- Setup: configuring mock GitLab for MR iid=1 ---")
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1,
            "title": "Add order processing",
            "description": "Implements order handler",
            "author": {"username": "alice"},
            "source_branch": "feature/orders",
            "target_branch": "main",
            "sha": "bbb222",
            "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{
                "old_path": "src/handler.go",
                "new_path": "src/handler.go",
                "diff": "@@ -10,6 +10,12 @@ package handler\n import \"fmt\"\n \n+func ProcessOrder(order *Order) error {\n+    result := CalculateTotal(order.Items)\n+    if result == nil {\n+        return nil\n+    }\n+    fmt.Println(result)\n+    return nil\n+}",
                "new_file": false, "deleted_file": false, "renamed_file": false
            }]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1,
            "head_commit_sha": "bbb222",
            "base_commit_sha": "aaa111",
            "start_commit_sha": "aaa111"
        }]`),
	})
	llm.DefaultResponse = defaultLLMResponse
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	t.Log("--- Sending webhook for MR iid=1, action=open ---")
	resp := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("webhook: expected 200, got %d", resp.StatusCode)
	}

	t.Log("--- Waiting for completed review run in DB ---")
	dbRun := WaitForReviewRun(t, repoID, 1, "completed", 90*time.Second)
	t.Logf("found completed run: id=%s", dbRun.ID)

	getResp, err := clients.Review.GetReviewRun(context.Background(),
		connect.NewRequest(&apiv1.GetReviewRunRequest{Id: dbRun.ID}))
	if err != nil {
		t.Fatalf("GetReviewRun: %v", err)
	}
	run := getResp.Msg.ReviewRun

	// A1: Status
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}

	// A2: Comment count
	t.Logf("A2: %d comments", len(run.Comments))
	if len(run.Comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(run.Comments))
	}

	// A8: Summary note posted
	notes := gitlab.Notes()
	t.Logf("A8: %d summary notes posted", len(notes))
	if len(notes) != 1 {
		t.Fatalf("expected 1 posted note, got %d", len(notes))
	}
	if !strings.Contains(notes[0].Body, "nil pointer") {
		t.Errorf("summary note missing 'nil pointer': %s", notes[0].Body)
	}

	// A9: Inline discussions posted
	discussions := gitlab.Discussions()
	t.Logf("A9: %d inline discussions posted", len(discussions))
	if len(discussions) != 2 {
		t.Fatalf("expected 2 posted discussions, got %d", len(discussions))
	}

	// A11: LLM called once
	if llm.RequestCount() != 1 {
		t.Errorf("expected 1 LLM request, got %d", llm.RequestCount())
	}
}

// TestInvalidWebhookSecret verifies webhook authentication rejects bad/missing secrets.
func TestInvalidWebhookSecret(t *testing.T) {
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	providerID, _, _ := SetupProviderAndRepo(t, clients, gitlab)

	payload := map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "open", "draft": false, "work_in_progress": false,
		},
	}

	// Wrong secret
	resp := SendWebhook(t, clients.BaseURL, providerID, "wrong-secret", payload)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("wrong secret: expected 401, got %d", resp.StatusCode)
	}

	// Empty secret
	resp2 := SendWebhook(t, clients.BaseURL, providerID, "", payload)
	resp2.Body.Close()
	if resp2.StatusCode != 401 {
		t.Errorf("empty secret: expected 401, got %d", resp2.StatusCode)
	}

	time.Sleep(3 * time.Second)
	if llm.RequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", llm.RequestCount())
	}
}

// TestUnknownRepoWebhook verifies webhook for an unregistered project is accepted but produces no review.
func TestUnknownRepoWebhook(t *testing.T) {
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	// SetupProviderAndRepo registers project 100; we'll send a webhook for project 999
	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	resp := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 999},
		"object_attributes": map[string]any{
			"iid": 1, "action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	time.Sleep(3 * time.Second)
	if llm.RequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", llm.RequestCount())
	}
	runs := QueryReviewRuns(t, repoID, 1)
	if len(runs) != 0 {
		t.Errorf("expected 0 review runs for project 999, got %d", len(runs))
	}
}

// TestDraftMRNoReview verifies draft MRs are recorded but not reviewed.
func TestDraftMRNoReview(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1, "title": "WIP: draft PR", "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/wip", "target_branch": "main",
            "sha": "draft111", "draft": true
        }`),
		Changes:  json.RawMessage(`{"changes":[]}`),
		Versions: json.RawMessage(`[]`),
	})
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	resp := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "open", "draft": true, "work_in_progress": true,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Draft run should appear in DB quickly (no Restate dispatch needed)
	WaitForReviewRun(t, repoID, 1, "draft", 15*time.Second)

	if llm.RequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", llm.RequestCount())
	}
	if len(gitlab.Notes()) != 0 {
		t.Errorf("expected 0 notes, got %d", len(gitlab.Notes()))
	}
	if len(gitlab.Discussions()) != 0 {
		t.Errorf("expected 0 discussions, got %d", len(gitlab.Discussions()))
	}
}

// TestDraftToReadyTransition verifies unmarking a draft MR triggers a full review.
func TestDraftToReadyTransition(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1,
            "title": "Add order processing",
            "description": "Implements order handler",
            "author": {"username": "alice"},
            "source_branch": "feature/orders",
            "target_branch": "main",
            "sha": "bbb222",
            "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{
                "old_path": "src/handler.go",
                "new_path": "src/handler.go",
                "diff": "@@ -10,6 +10,12 @@ package handler\n import \"fmt\"\n \n+func ProcessOrder(order *Order) error {\n+    result := CalculateTotal(order.Items)\n+    if result == nil {\n+        return nil\n+    }\n+    fmt.Println(result)\n+    return nil\n+}",
                "new_file": false, "deleted_file": false, "renamed_file": false
            }]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1,
            "head_commit_sha": "bbb222",
            "base_commit_sha": "aaa111",
            "start_commit_sha": "aaa111"
        }]`),
	})
	llm.DefaultResponse = defaultLLMResponse
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	// Step 1: Send draft webhook
	resp := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "open", "draft": true, "work_in_progress": true,
		},
	})
	resp.Body.Close()
	WaitForReviewRun(t, repoID, 1, "draft", 15*time.Second)
	if llm.RequestCount() != 0 {
		t.Errorf("expected 0 LLM calls after draft webhook, got %d", llm.RequestCount())
	}

	// Step 2: Send draft→ready transition webhook
	resp2 := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "update", "draft": false, "work_in_progress": false,
		},
		"changes": map[string]any{
			"draft": map[string]any{"previous": true, "current": false},
		},
	})
	resp2.Body.Close()

	// Wait for completed run
	dbRun := WaitForReviewRun(t, repoID, 1, "completed", 90*time.Second)
	t.Logf("completed run id=%s", dbRun.ID)

	if llm.RequestCount() != 1 {
		t.Errorf("expected 1 LLM call total, got %d", llm.RequestCount())
	}
	notes := gitlab.Notes()
	if len(notes) == 0 {
		t.Errorf("expected at least 1 summary note posted")
	}
}

// TestReviewDisabledRepo verifies webhooks for repos with review disabled produce no run.
func TestReviewDisabledRepo(t *testing.T) {
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	_, err := clients.Repo.DisableReview(context.Background(),
		connect.NewRequest(&apiv1.DisableReviewRequest{RepoId: repoID}))
	if err != nil {
		t.Fatalf("DisableReview: %v", err)
	}

	resp := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	time.Sleep(3 * time.Second)
	runs := QueryReviewRuns(t, repoID, 1)
	if len(runs) != 0 {
		t.Errorf("expected 0 review runs, got %d", len(runs))
	}
	if llm.RequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", llm.RequestCount())
	}
}

// TestLargeDiffShortCircuit verifies diffs >5000 lines skip LLM and post a canned message.
func TestLargeDiffShortCircuit(t *testing.T) {
	// Generate a diff with 5001 changed lines (each starts with '+')
	var sb strings.Builder
	sb.WriteString("@@ -1,0 +1,5001 @@\n")
	for i := 0; i < 5001; i++ {
		fmt.Fprintf(&sb, "+line%d\n", i)
	}
	largeDiff := sb.String()

	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1, "title": "Huge refactor", "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/large", "target_branch": "main",
            "sha": "large111", "draft": false
        }`),
		Changes: json.RawMessage(fmt.Sprintf(`{
            "changes": [{"old_path": "big.go", "new_path": "big.go", "diff": %q,
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`, largeDiff)),
		Versions: json.RawMessage(`[{
            "id": 1, "head_commit_sha": "large111",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	llm.DefaultResponse = defaultLLMResponse
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	_, repoID, _ := SetupProviderAndRepo(t, clients, gitlab)

	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{RepoId: repoID, MrNumber: 1}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id

	run := PollReviewRun(t, clients.Review, runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 60*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}
	if llm.RequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", llm.RequestCount())
	}
	notes := gitlab.Notes()
	if len(notes) != 1 {
		t.Fatalf("expected 1 note, got %d", len(notes))
	}
	if !strings.Contains(strings.ToLower(notes[0].Body), "too large") {
		t.Errorf("note body missing 'too large': %s", notes[0].Body)
	}
	if len(gitlab.Discussions()) != 0 {
		t.Errorf("expected 0 discussions, got %d", len(gitlab.Discussions()))
	}
}

// TestDuplicateDiffDedup verifies the same diff hash sent twice produces only one review.
func TestDuplicateDiffDedup(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1,
            "title": "Add order processing",
            "description": "Implements order handler",
            "author": {"username": "alice"},
            "source_branch": "feature/orders",
            "target_branch": "main",
            "sha": "dedup222",
            "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{
                "old_path": "src/handler.go",
                "new_path": "src/handler.go",
                "diff": "@@ -1,3 +1,4 @@\n package handler\n+// dedup test\n func Foo() {}",
                "new_file": false, "deleted_file": false, "renamed_file": false
            }]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1,
            "head_commit_sha": "dedup222",
            "base_commit_sha": "aaa111",
            "start_commit_sha": "aaa111"
        }]`),
	})
	llm.DefaultResponse = defaultLLMResponse
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	webhookPayload := map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "open", "draft": false, "work_in_progress": false,
		},
	}

	// First webhook — wait for it to complete before sending second
	t.Log("sending first webhook, waiting for completion...")
	resp := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, webhookPayload)
	resp.Body.Close()
	WaitForReviewRun(t, repoID, 1, "completed", 90*time.Second)
	if llm.RequestCount() != 1 {
		t.Errorf("expected 1 LLM call after first webhook, got %d", llm.RequestCount())
	}

	// Second identical webhook — same sha, dedup should mark run as skipped
	// Smart debounce may add ~3 min delay before the second invocation processes
	t.Log("sending second webhook (same sha), waiting for dedup skipped status...")
	resp2 := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, webhookPayload)
	resp2.Body.Close()
	WaitForReviewRun(t, repoID, 1, "skipped", 240*time.Second)

	if llm.RequestCount() != 1 {
		t.Errorf("expected 1 total LLM call (second was deduped), got %d", llm.RequestCount())
	}
}

// TestProviderDeletionCascade verifies deleting a provider soft-deletes repos and rejects future webhooks.
func TestProviderDeletionCascade(t *testing.T) {
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	// Delete provider
	_, err := clients.Provider.DeleteProvider(context.Background(),
		connect.NewRequest(&apiv1.DeleteProviderRequest{Id: providerID}))
	if err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}

	// Provider should not appear in list
	listResp, err := clients.Provider.ListProviders(context.Background(),
		connect.NewRequest(&apiv1.ListProvidersRequest{}))
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	for _, p := range listResp.Msg.Providers {
		if p.Id == providerID {
			t.Errorf("deleted provider still in list")
		}
	}

	// Repos for deleted provider should be empty
	listReposResp, err := clients.Repo.ListRepos(context.Background(),
		connect.NewRequest(&apiv1.ListReposRequest{ProviderId: providerID}))
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(listReposResp.Msg.Repositories) != 0 {
		t.Errorf("expected 0 repos for deleted provider, got %d", len(listReposResp.Msg.Repositories))
	}

	// Webhook for deleted provider should return 404 (provider not found)
	resp := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("expected 404 for deleted provider webhook, got %d", resp.StatusCode)
	}

	time.Sleep(2 * time.Second)
	runs := QueryReviewRuns(t, repoID, 1)
	if len(runs) != 0 {
		t.Errorf("expected 0 review runs after provider deletion, got %d", len(runs))
	}
	if llm.RequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", llm.RequestCount())
	}
}

// TestLLMTerminalError verifies LLM HTTP 400 errors result in a FAILED review run.
func TestLLMTerminalError(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1,
            "title": "Add feature",
            "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/x", "target_branch": "main",
            "sha": "err111", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "x.go", "new_path": "x.go",
            "diff": "@@ -1,2 +1,3 @@\n package x\n+// new line\n func F() {}",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1, "head_commit_sha": "err111",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	// Set ResponseFunc AFTER the reset so it takes effect for this test
	llm.ResponseFunc = func(reqBody []byte) (int, json.RawMessage) {
		return 400, json.RawMessage(`{"error":{"message":"invalid_request","type":"invalid_request_error"}}`)
	}

	_, repoID, _ := SetupProviderAndRepo(t, clients, gitlab)

	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{RepoId: repoID, MrNumber: 1}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id

	run := PollReviewRun(t, clients.Review, runID,
		apiv1.ReviewStatus_REVIEW_STATUS_FAILED, 120*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_FAILED {
		t.Errorf("expected FAILED, got %s", run.Status)
	}
	if len(gitlab.Notes()) != 0 {
		t.Errorf("expected 0 notes, got %d", len(gitlab.Notes()))
	}
	if len(gitlab.Discussions()) != 0 {
		t.Errorf("expected 0 discussions, got %d", len(gitlab.Discussions()))
	}
}

// TestGitLab404ForMR verifies GitLab returning 404 for MR fetch results in a FAILED review.
func TestGitLab404ForMR(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		StatusCode: 404,
		Details:    json.RawMessage(`{"message":"404 Not found"}`),
	})
	llm.DefaultResponse = defaultLLMResponse
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	_, repoID, _ := SetupProviderAndRepo(t, clients, gitlab)

	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{RepoId: repoID, MrNumber: 1}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id

	run := PollReviewRun(t, clients.Review, runID,
		apiv1.ReviewStatus_REVIEW_STATUS_FAILED, 60*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_FAILED {
		t.Errorf("expected FAILED, got %s", run.Status)
	}
	if llm.RequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", llm.RequestCount())
	}
	if len(gitlab.Notes()) != 0 {
		t.Errorf("expected 0 notes, got %d", len(gitlab.Notes()))
	}
}

// TestReadFileToolGracefulDegradation verifies that when the LLM calls the read_file tool
// for a file that doesn't exist, the tool returns a human-readable error and the review
// still completes successfully.
func TestReadFileToolGracefulDegradation(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1,
            "title": "Add order processing",
            "description": "Implements order handler",
            "author": {"username": "alice"},
            "source_branch": "feature/orders",
            "target_branch": "main",
            "sha": "bbb222",
            "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{
                "old_path": "src/handler.go",
                "new_path": "src/handler.go",
                "diff": "@@ -10,6 +10,12 @@ package handler\n import \"fmt\"\n \n+func ProcessOrder(order *Order) error {\n+    result := CalculateTotal(order.Items)\n+    if result == nil {\n+        return nil\n+    }\n+    fmt.Println(result)\n+    return nil\n+}",
                "new_file": false, "deleted_file": false, "renamed_file": false
            }]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1,
            "head_commit_sha": "bbb222",
            "base_commit_sha": "aaa111",
            "start_commit_sha": "aaa111"
        }]`),
	})
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	// Two-turn conversation: first call returns read_file tool call, second returns final_result.
	var callCount atomic.Int32
	llm.ResponseFunc = func(reqBody []byte) (int, json.RawMessage) {
		n := callCount.Add(1)
		if n == 1 {
			// LLM asks to read a nonexistent file
			return 200, json.RawMessage(`{
                "id": "chatcmpl-rf-1",
                "object": "chat.completion",
                "model": "test-model",
                "choices": [{
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": null,
                        "tool_calls": [{
                            "id": "call_rf1",
                            "type": "function",
                            "function": {
                                "name": "read_file",
                                "arguments": "{\"file_path\": \"nonexistent/file.go\"}"
                            }
                        }]
                    },
                    "finish_reason": "tool_calls"
                }],
                "usage": {"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120}
            }`)
		}
		// Second call: LLM received the error from read_file and produces final result
		return 200, defaultLLMResponse
	}

	_, repoID, _ := SetupProviderAndRepo(t, clients, gitlab)

	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{RepoId: repoID, MrNumber: 1}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id

	run := PollReviewRun(t, clients.Review, runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 60*time.Second, 2*time.Second)

	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}
	// LLM was called twice: once to get read_file call, once after receiving the error
	if callCount.Load() != 2 {
		t.Errorf("expected 2 LLM calls (tool call + final), got %d", callCount.Load())
	}
	// The second LLM request must contain the read_file tool result (error message about missing file)
	reqs := llm.Requests()
	if len(reqs) >= 2 {
		body := string(reqs[1].Body)
		if !strings.Contains(body, "Error:") {
			t.Errorf("second LLM request missing read_file error result: %s", body)
		}
	}
	// Review still posted comments despite the tool error
	if len(run.Comments) == 0 {
		t.Errorf("expected comments in completed review after read_file error")
	}
}

// TestSearchCodebaseToolGracefulDegradation verifies that when the LLM calls the search_codebase
// tool but search-mcp fails (e.g. embedding error), the tool returns a human-readable error
// and the review still completes successfully.
func TestSearchCodebaseToolGracefulDegradation(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1,
            "title": "Add order processing",
            "description": "Implements order handler",
            "author": {"username": "alice"},
            "source_branch": "feature/orders",
            "target_branch": "main",
            "sha": "bbb222",
            "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{
                "old_path": "src/handler.go",
                "new_path": "src/handler.go",
                "diff": "@@ -10,6 +10,12 @@ package handler\n import \"fmt\"\n \n+func ProcessOrder(order *Order) error {\n+    result := CalculateTotal(order.Items)\n+    if result == nil {\n+        return nil\n+    }\n+    fmt.Println(result)\n+    return nil\n+}",
                "new_file": false, "deleted_file": false, "renamed_file": false
            }]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1,
            "head_commit_sha": "bbb222",
            "base_commit_sha": "aaa111",
            "start_commit_sha": "aaa111"
        }]`),
	})
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	// Two-turn conversation: first call returns search_codebase tool call, second returns final_result.
	// After the first LLM call (indexing is done by then), break embeddings so search-mcp fails.
	var callCount atomic.Int32
	llm.ResponseFunc = func(reqBody []byte) (int, json.RawMessage) {
		n := callCount.Add(1)
		if n == 1 {
			// Break embeddings so search-mcp can't embed the query (422 = non-retryable)
			llm.SetEmbeddingResponseFunc(func(rb []byte) (int, json.RawMessage) {
				return 422, json.RawMessage(`{"error":{"message":"embedding service unavailable"}}`)
			})
			// LLM asks to search the codebase
			return 200, json.RawMessage(`{
                "id": "chatcmpl-sc-1",
                "object": "chat.completion",
                "model": "test-model",
                "choices": [{
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": null,
                        "tool_calls": [{
                            "id": "call_sc1",
                            "type": "function",
                            "function": {
                                "name": "search_codebase",
                                "arguments": "{\"query\": \"CalculateTotal function definition\"}"
                            }
                        }]
                    },
                    "finish_reason": "tool_calls"
                }],
                "usage": {"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120}
            }`)
		}
		// Second call: LLM received the error from search_codebase and produces final result
		return 200, defaultLLMResponse
	}

	_, repoID, _ := SetupProviderAndRepo(t, clients, gitlab)

	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{RepoId: repoID, MrNumber: 1}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id

	run := PollReviewRun(t, clients.Review, runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 60*time.Second, 2*time.Second)

	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}
	// LLM was called twice: once to get search_codebase call, once after receiving the error
	if callCount.Load() != 2 {
		t.Errorf("expected 2 LLM calls (tool call + final), got %d", callCount.Load())
	}
	// The second LLM request must contain the search_codebase tool result (error from search-mcp)
	reqs := llm.Requests()
	if len(reqs) >= 2 {
		body := string(reqs[1].Body)
		if !strings.Contains(body, "search-mcp failed") {
			t.Errorf("second LLM request missing search_codebase error result: %s", body)
		}
	}
	// Review still posted comments despite the tool error
	if len(run.Comments) == 0 {
		t.Errorf("expected comments in completed review after search_codebase error")
	}
}

// TestSemanticSearch verifies the reviewer uses the search_codebase tool to find a function
// definition not in the diff, and posts inline comments based on the search result.
func TestSemanticSearch(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1,
            "title": "Call mathutil.Foo",
            "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/semantic", "target_branch": "main",
            "sha": "sem111", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "cmd/main.go", "new_path": "cmd/main.go",
            "diff": "@@ -8,6 +8,8 @@ package main\n import \"pkg/mathutil\"\n \n func main() {\n+    result := mathutil.Foo(42)\n+    _ = result\n }",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1, "head_commit_sha": "sem111",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	// Two-turn conversation: first call returns search_codebase tool call,
	// second returns final_result with a comment about wrong argument count.
	var callCount atomic.Int32
	llm.ResponseFunc = func(reqBody []byte) (int, json.RawMessage) {
		n := callCount.Add(1)
		if n == 1 {
			return 200, json.RawMessage(`{
                "id": "chatcmpl-sem-1",
                "object": "chat.completion",
                "model": "test-model",
                "choices": [{
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": null,
                        "tool_calls": [{
                            "id": "call_sem",
                            "type": "function",
                            "function": {
                                "name": "search_codebase",
                                "arguments": "{\"query\": \"Foo function definition\"}"
                            }
                        }]
                    },
                    "finish_reason": "tool_calls"
                }],
                "usage": {"prompt_tokens": 200, "completion_tokens": 50, "total_tokens": 250}
            }`)
		}
		return 200, json.RawMessage(`{
            "id": "chatcmpl-sem-2",
            "object": "chat.completion",
            "model": "test-model",
            "choices": [{
                "index": 0,
                "message": {
                    "role": "assistant",
                    "tool_calls": [{
                        "id": "call_sem_final",
                        "type": "function",
                        "function": {
                            "name": "final_result",
                            "arguments": "{\"summary\":\"Wrong argument count.\",\"comments\":[{\"file_path\":\"cmd/main.go\",\"line_start\":11,\"line_end\":11,\"body\":\"mathutil.Foo requires 2 arguments (x int, y int) but is called with only 1\"}]}"
                        }
                    }]
                },
                "finish_reason": "stop"
            }],
            "usage": {"prompt_tokens": 300, "completion_tokens": 50, "total_tokens": 350}
        }`)
	}

	_, repoID, _ := SetupProviderAndRepo(t, clients, gitlab)

	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{RepoId: repoID, MrNumber: 1}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id

	run := PollReviewRun(t, clients.Review, runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 60*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}
	discussions := gitlab.Discussions()
	if len(discussions) != 1 {
		t.Fatalf("expected 1 inline discussion, got %d", len(discussions))
	}
	if !strings.Contains(discussions[0].Body, "mathutil.Foo requires 2 arguments") {
		t.Errorf("discussion body missing expected content: %s", discussions[0].Body)
	}
	// The second LLM request should contain the search tool result
	llmReqs := llm.Requests()
	if len(llmReqs) < 2 {
		t.Fatalf("expected at least 2 LLM requests (search + final), got %d", len(llmReqs))
	}
	// Verify the search_codebase tool result was passed back to the LLM
	body := string(llmReqs[1].Body)
	if !strings.Contains(body, "search_codebase") && !strings.Contains(body, "tool") {
		t.Errorf("second LLM request missing search_codebase tool result: %s", body)
	}
}

// TestRepoSyncerCloneFailure verifies that when RepoSyncer cannot clone the repo
// (no git server at that path), the review run reaches FAILED status.
func TestRepoSyncerCloneFailure(t *testing.T) {
	// Register a second project (ID=200) that has no git repo served by the mock.
	// The mock only serves group/test-project.git; nonexistent/broken-repo.git returns 404.
	gitlab.SetProjects([]GitLabProject{
		{ID: 100, Name: "test-project", PathWithNamespace: "group/test-project", HTTPURLToRepo: "http://gitlab.example.com/group/test-project.git", DefaultBranch: "main"},
		{ID: 200, Name: "broken-repo", PathWithNamespace: "nonexistent/broken-repo", HTTPURLToRepo: "http://gitlab.example.com/nonexistent/broken-repo.git", DefaultBranch: "main"},
	})
	gitlab.SetMR("200", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1,
            "title": "Some change",
            "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/x", "target_branch": "main",
            "sha": "abc123", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "x.go", "new_path": "x.go",
            "diff": "@@ -1,2 +1,3 @@\n package x\n+// new line\n func F() {}",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1, "head_commit_sha": "abc123",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	llm.DefaultResponse = defaultLLMResponse
	t.Cleanup(func() {
		gitlab.Reset()
		llm.Reset()
		// Restore original project list
		gitlab.SetProjects([]GitLabProject{
			{ID: 100, Name: "test-project", PathWithNamespace: "group/test-project", HTTPURLToRepo: "http://gitlab.example.com/group/test-project.git", DefaultBranch: "main"},
		})
	})
	gitlab.Reset()
	llm.Reset()

	// Create a second provider pointing at the same mock GitLab (which now returns project 200)
	createResp, err := clients.Provider.CreateProvider(context.Background(),
		connect.NewRequest(&apiv1.CreateProviderRequest{
			Type:    apiv1.ProviderType_PROVIDER_TYPE_GITLAB_SELF_HOSTED,
			Name:    "broken-provider",
			BaseUrl: gitlab.HostURL(),
			Token:   "test-token",
		}))
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	providerID := createResp.Msg.Provider.Id

	// Find repo with remoteId=200
	repos := waitForRepos(t, clients.Repo, providerID, 15*time.Second)
	var repoID string
	for _, repo := range repos {
		if repo.RemoteId == "200" {
			repoID = repo.Id
			break
		}
	}
	if repoID == "" {
		t.Fatal("repo with remoteId=200 not found")
	}

	_, err = clients.Repo.EnableReview(context.Background(),
		connect.NewRequest(&apiv1.EnableReviewRequest{RepoId: repoID}))
	if err != nil {
		t.Fatalf("EnableReview: %v", err)
	}

	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{RepoId: repoID, MrNumber: 1}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id

	run := PollReviewRun(t, clients.Review, runID,
		apiv1.ReviewStatus_REVIEW_STATUS_FAILED, 120*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_FAILED {
		t.Errorf("expected FAILED (clone error), got %s", run.Status)
	}
	if llm.RequestCount() != 0 {
		t.Errorf("expected 0 LLM calls when SyncRepo fails, got %d", llm.RequestCount())
	}
	if len(gitlab.Notes()) != 0 {
		t.Errorf("expected 0 notes when SyncRepo fails, got %d", len(gitlab.Notes()))
	}
	if len(gitlab.Discussions()) != 0 {
		t.Errorf("expected 0 discussions when SyncRepo fails, got %d", len(gitlab.Discussions()))
	}
}

// TestIndexerFailureGracefulDegradation verifies that when IndexRepo fails (embeddings return 500),
// the review still completes without semantic search capability.
func TestIndexerFailureGracefulDegradation(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1,
            "title": "Add order processing",
            "description": "Implements order handler",
            "author": {"username": "alice"},
            "source_branch": "feature/orders",
            "target_branch": "main",
            "sha": "bbb222",
            "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{
                "old_path": "src/handler.go",
                "new_path": "src/handler.go",
                "diff": "@@ -10,6 +10,12 @@ package handler\n import \"fmt\"\n \n+func ProcessOrder(order *Order) error {\n+    result := CalculateTotal(order.Items)\n+    if result == nil {\n+        return nil\n+    }\n+    fmt.Println(result)\n+    return nil\n+}",
                "new_file": false, "deleted_file": false, "renamed_file": false
            }]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1,
            "head_commit_sha": "bbb222",
            "base_commit_sha": "aaa111",
            "start_commit_sha": "aaa111"
        }]`),
	})
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	// Embeddings always return 422 (non-retryable) → IndexRepo will fail
	llm.EmbeddingResponseFunc = func(reqBody []byte) (int, json.RawMessage) {
		return 422, json.RawMessage(`{"error":{"message":"embedding service unavailable"}}`)
	}

	// Two-turn conversation: first call returns search_codebase (which should fail gracefully),
	// second returns final_result.
	var callCount atomic.Int32
	llm.ResponseFunc = func(reqBody []byte) (int, json.RawMessage) {
		n := callCount.Add(1)
		if n == 1 {
			return 200, json.RawMessage(`{
                "id": "chatcmpl-idx-1",
                "object": "chat.completion",
                "model": "test-model",
                "choices": [{
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": null,
                        "tool_calls": [{
                            "id": "call_idx1",
                            "type": "function",
                            "function": {
                                "name": "search_codebase",
                                "arguments": "{\"query\": \"CalculateTotal function definition\"}"
                            }
                        }]
                    },
                    "finish_reason": "tool_calls"
                }],
                "usage": {"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120}
            }`)
		}
		return 200, defaultLLMResponse
	}

	_, repoID, _ := SetupProviderAndRepo(t, clients, gitlab)

	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{RepoId: repoID, MrNumber: 1}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id

	// Review should complete even when indexing fails (graceful degradation)
	run := PollReviewRun(t, clients.Review, runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 90*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED (indexing failure should degrade gracefully), got %s", run.Status)
	}
	if callCount.Load() != 2 {
		t.Errorf("expected 2 LLM calls (search + final), got %d", callCount.Load())
	}
	// Second request must contain "search context not available" (indexing failed → no collection)
	reqs := llm.Requests()
	if len(reqs) >= 2 {
		body := string(reqs[1].Body)
		if !strings.Contains(body, "search context not available") {
			t.Errorf("second LLM request should have 'search context not available' (indexing failed): %s", body)
		}
	}
	// Comments still posted despite indexing failure
	if len(run.Comments) == 0 {
		t.Errorf("expected comments in completed review despite indexing failure")
	}
}

// TestReadFileToolWorksWithSyncedRepo verifies that after SyncRepo, the read_file tool
// reads from the correct repo_path and returns real file content.
func TestReadFileToolWorksWithSyncedRepo(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1,
            "title": "Add order processing",
            "description": "Implements order handler",
            "author": {"username": "alice"},
            "source_branch": "feature/orders",
            "target_branch": "main",
            "sha": "bbb222",
            "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{
                "old_path": "src/handler.go",
                "new_path": "src/handler.go",
                "diff": "@@ -10,6 +10,12 @@ package handler\n import \"fmt\"\n \n+func ProcessOrder(order *Order) error {\n+    result := CalculateTotal(order.Items)\n+    if result == nil {\n+        return nil\n+    }\n+    fmt.Println(result)\n+    return nil\n+}",
                "new_file": false, "deleted_file": false, "renamed_file": false
            }]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1,
            "head_commit_sha": "bbb222",
            "base_commit_sha": "aaa111",
            "start_commit_sha": "aaa111"
        }]`),
	})
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	// Two-turn: first call asks to read src/util.go (which exists in the bare repo),
	// second call returns final_result using the file content.
	var callCount atomic.Int32
	llm.ResponseFunc = func(reqBody []byte) (int, json.RawMessage) {
		n := callCount.Add(1)
		if n == 1 {
			return 200, json.RawMessage(`{
                "id": "chatcmpl-rf-synced-1",
                "object": "chat.completion",
                "model": "test-model",
                "choices": [{
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": null,
                        "tool_calls": [{
                            "id": "call_rf_synced1",
                            "type": "function",
                            "function": {
                                "name": "read_file",
                                "arguments": "{\"file_path\": \"src/util.go\"}"
                            }
                        }]
                    },
                    "finish_reason": "tool_calls"
                }],
                "usage": {"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120}
            }`)
		}
		return 200, defaultLLMResponse
	}

	_, repoID, _ := SetupProviderAndRepo(t, clients, gitlab)

	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{RepoId: repoID, MrNumber: 1}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id

	run := PollReviewRun(t, clients.Review, runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 90*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}
	if callCount.Load() != 2 {
		t.Errorf("expected 2 LLM calls (read_file + final), got %d", callCount.Load())
	}

	// The second LLM request must contain the actual file content from the bare repo
	reqs := llm.Requests()
	if len(reqs) >= 2 {
		body := string(reqs[1].Body)
		// "CalculateTotal" is a known unique string in src/util.go
		if !strings.Contains(body, "CalculateTotal") {
			t.Errorf("second LLM request should contain src/util.go content ('CalculateTotal'): %s", body)
		}
		// Must NOT contain the degradation error — repo_path should be populated
		if strings.Contains(body, "repository context not available") {
			t.Errorf("second LLM request should not contain degradation error — repo_path should be populated")
		}
	}
	if len(run.Comments) == 0 {
		t.Errorf("expected comments in completed review")
	}
}

// TestDisableReviewStopsWebhook verifies that disabling review stops new webhook-triggered reviews.
func TestDisableReviewStopsWebhook(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1, "title": "Fix bug", "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/fix", "target_branch": "main",
            "sha": "disab111", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "fix.go", "new_path": "fix.go",
            "diff": "@@ -1,2 +1,3 @@\n package main\n+// fix\n func F() {}",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1, "head_commit_sha": "disab111",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	llm.DefaultResponse = defaultLLMResponse
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	// First webhook → review completes
	resp := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("first webhook: expected 200, got %d", resp.StatusCode)
	}
	WaitForReviewRun(t, repoID, 1, "completed", 90*time.Second)
	if llm.RequestCount() != 1 {
		t.Errorf("expected 1 LLM call after first webhook, got %d", llm.RequestCount())
	}

	// Record run count before disabling
	runsAfterFirst := QueryReviewRuns(t, repoID, 1)

	// Disable review
	_, err := clients.Repo.DisableReview(context.Background(),
		connect.NewRequest(&apiv1.DisableReviewRequest{RepoId: repoID}))
	if err != nil {
		t.Fatalf("DisableReview: %v", err)
	}

	// Second webhook — should be ignored since review is disabled
	resp2 := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "update", "draft": false, "work_in_progress": false,
		},
	})
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("webhook after disable: expected 200, got %d", resp2.StatusCode)
	}

	time.Sleep(3 * time.Second)
	if llm.RequestCount() != 1 {
		t.Errorf("expected 1 total LLM call (second webhook ignored after disable), got %d", llm.RequestCount())
	}
	runsAfterSecond := QueryReviewRuns(t, repoID, 1)
	if len(runsAfterSecond) != len(runsAfterFirst) {
		t.Errorf("expected no new review runs after disable, got %d extra",
			len(runsAfterSecond)-len(runsAfterFirst))
	}
}

// TestReReviewOnNewPush verifies a new push to an existing MR triggers a fresh review.
func TestReReviewOnNewPush(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1, "title": "Feature branch", "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/x", "target_branch": "main",
            "sha": "rerv1sha0", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "a.go", "new_path": "a.go",
            "diff": "@@ -1,2 +1,3 @@\n package a\n+// push1\n func A() {}",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1, "head_commit_sha": "rerv1sha0",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	llm.DefaultResponse = defaultLLMResponse
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	// First webhook
	resp := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()
	WaitForReviewRun(t, repoID, 1, "completed", 90*time.Second)
	if llm.RequestCount() != 1 {
		t.Errorf("expected 1 LLM call after first push, got %d", llm.RequestCount())
	}

	// Update MR to a new SHA (simulates new push with different changes)
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1, "title": "Feature branch", "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/x", "target_branch": "main",
            "sha": "rerv2sha0", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "a.go", "new_path": "a.go",
            "diff": "@@ -1,2 +1,3 @@\n package a\n+// push2 changed content\n func A() {}",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 2, "head_commit_sha": "rerv2sha0",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})

	// Second webhook (new push, different SHA — no debounce since first completed normally)
	resp2 := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "update", "draft": false, "work_in_progress": false,
		},
	})
	resp2.Body.Close()

	// Wait for second completed run (no debounce since first completed normally)
	t.Log("waiting for second completed run...")
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		runs := QueryReviewRuns(t, repoID, 1)
		completedCount := 0
		for _, r := range runs {
			if r.Status == "completed" {
				completedCount++
			}
		}
		if completedCount >= 2 {
			break
		}
		time.Sleep(2 * time.Second)
	}

	runs := QueryReviewRuns(t, repoID, 1)
	var completedRuns []DBReviewRun
	for _, r := range runs {
		if r.Status == "completed" {
			completedRuns = append(completedRuns, r)
		}
	}
	if len(completedRuns) != 2 {
		t.Fatalf("expected 2 completed runs (one per push), got %d", len(completedRuns))
	}
	if completedRuns[0].DiffHash == completedRuns[1].DiffHash {
		t.Errorf("expected different diff hashes for two different pushes, got same: %s", completedRuns[0].DiffHash)
	}
	if llm.RequestCount() != 2 {
		t.Errorf("expected 2 LLM calls (one per push), got %d", llm.RequestCount())
	}
}

// TestCancelOnNewPush verifies that a new webhook while a review is in-flight cancels the first
// and only the second review completes (after debounce).
func TestCancelOnNewPush(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1, "title": "Cancel test v1", "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/cancel", "target_branch": "main",
            "sha": "canc1v1s0", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "c.go", "new_path": "c.go",
            "diff": "@@ -1,2 +1,3 @@\n package c\n+// cancel v1\n func C() {}",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1, "head_commit_sha": "canc1v1s0",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	// Block the first LLM response so the first review is in-flight when the second webhook arrives.
	// The second webhook triggers Restate to cancel the first invocation and start a new one.
	// After cancellation is triggered, we unblock the first LLM call - it should return normally,
	// but the Reviewer should stop processing because Restate closed the connection.
	unblockFirstLLM := make(chan struct{})
	firstCallReturned := make(chan struct{})
	var callCount atomic.Int32
	llm.ResponseFunc = func(reqBody []byte) (int, json.RawMessage) {
		n := callCount.Add(1)
		if n == 1 {
			// First call: block until we're ready to proceed
			<-unblockFirstLLM
			close(firstCallReturned)
		}
		// Always return normal 200 response - LLM doesn't know about cancellation
		return 200, defaultLLMResponse
	}

	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	// Send first webhook
	resp := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()

	// Poll until first LLM call received (first review is now in-flight, blocked at LLM)
	t.Log("waiting for first LLM call (first review in-flight)...")
	pollDeadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(pollDeadline) {
		if llm.RequestCount() >= 1 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if llm.RequestCount() < 1 {
		close(unblockFirstLLM)
		t.Fatal("first LLM call not received within 60s")
	}
	t.Log("first LLM call received; sending second webhook to trigger Restate cancellation")

	// Update MR to new SHA and send second webhook (cancels first in-flight review)
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1, "title": "Cancel test v2", "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/cancel", "target_branch": "main",
            "sha": "canc1v2s0", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "c.go", "new_path": "c.go",
            "diff": "@@ -1,2 +1,3 @@\n package c\n+// cancel v2 supersedes\n func C() {}",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 2, "head_commit_sha": "canc1v2s0",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	resp2 := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "update", "draft": false, "work_in_progress": false,
		},
	})
	resp2.Body.Close()

	// Unblock the first LLM call. Restate should have cancelled the first invocation by now,
	// so even though the LLM returns 200, the Reviewer should not process the result.
	close(unblockFirstLLM)

	// Wait a moment for the first LLM call to complete
	select {
	case <-firstCallReturned:
	case <-time.After(5 * time.Second):
		t.Log("warning: first LLM call did not return within 5s")
	}

	// Wait for the second review to complete (after debounce + review pipeline).
	WaitForReviewRun(t, repoID, 1, "completed", 60*time.Second)

	// Only one review should have completed successfully.
	// The first invocation was cancelled by Restate and should not complete.
	runs := QueryReviewRuns(t, repoID, 1)
	completedCount := 0
	for _, r := range runs {
		if r.Status == "completed" {
			completedCount++
		}
	}
	if completedCount != 1 {
		t.Errorf("expected 1 completed run (first cancelled by Restate), got %d completed", completedCount)
	}
	// Two LLM calls expected: one for the first attempt (cancelled), one for the second (completed).
	if llm.RequestCount() < 2 {
		t.Errorf("expected at least 2 LLM calls (first cancelled + second completed), got %d", llm.RequestCount())
	}
}

// TestSingleRunPerWebhookReview verifies that webhook-triggered reviews create exactly ONE run
// with the correct restate_invocation_id set (regression test for double-run bug).
func TestSingleRunPerWebhookReview(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1, "title": "Single run test", "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/single", "target_branch": "main",
            "sha": "singlerun1", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "single.go", "new_path": "single.go",
            "diff": "@@ -1,2 +1,3 @@\n package single\n+// single run test\n func Single() {}",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1, "head_commit_sha": "singlerun1",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	llm.DefaultResponse = defaultLLMResponse
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	// Send webhook
	resp := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()

	// Wait for review to complete
	WaitForReviewRun(t, repoID, 1, "completed", 120*time.Second)

	// Verify only ONE run was created
	runs := QueryReviewRuns(t, repoID, 1)
	if len(runs) != 1 {
		t.Errorf("expected exactly 1 review run, got %d runs: %+v", len(runs), runs)
	}

	// Verify the run has restate_invocation_id set (not NULL)
	if len(runs) > 0 {
		run := runs[0]
		if run.RestateInvocationID == nil || *run.RestateInvocationID == "" {
			t.Errorf("expected run to have restate_invocation_id set, got nil or empty")
		} else {
			t.Logf("✓ Run has restate_invocation_id: %s", *run.RestateInvocationID)
		}
	}

	t.Logf("✓ Webhook-triggered review created exactly 1 run with invocation ID")
}

// TestConcurrentMRReviews verifies two different MRs on the same repo are reviewed independently.
func TestConcurrentMRReviews(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1, "title": "Feature A", "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/a", "target_branch": "main",
            "sha": "concsha10", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "a.go", "new_path": "a.go",
            "diff": "@@ -1,2 +1,3 @@\n package a\n+// concurrent MR1\n func A() {}",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1, "head_commit_sha": "concsha10",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	gitlab.SetMR("100", "2", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 2, "title": "Feature B", "description": "",
            "author": {"username": "bob"},
            "source_branch": "feature/b", "target_branch": "main",
            "sha": "concsha20", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "b.go", "new_path": "b.go",
            "diff": "@@ -1,2 +1,3 @@\n package b\n+// concurrent MR2\n func B() {}",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1, "head_commit_sha": "concsha20",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	llm.DefaultResponse = defaultLLMResponse
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	// Send webhooks for both MRs near-simultaneously
	resp1 := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp1.Body.Close()
	resp2 := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 2, "action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp2.Body.Close()
	if resp1.StatusCode != 200 || resp2.StatusCode != 200 {
		t.Fatalf("webhook status: MR1=%d, MR2=%d (expected both 200)", resp1.StatusCode, resp2.StatusCode)
	}

	// Wait for both MRs to complete (they run in parallel in Restate)
	t.Log("waiting for both MR reviews to complete concurrently...")
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		r1 := QueryReviewRuns(t, repoID, 1)
		r2 := QueryReviewRuns(t, repoID, 2)
		mr1Done, mr2Done := false, false
		for _, r := range r1 {
			if r.Status == "completed" {
				mr1Done = true
			}
		}
		for _, r := range r2 {
			if r.Status == "completed" {
				mr2Done = true
			}
		}
		if mr1Done && mr2Done {
			break
		}
		time.Sleep(2 * time.Second)
	}

	r1 := QueryReviewRuns(t, repoID, 1)
	r2 := QueryReviewRuns(t, repoID, 2)
	mr1Completed, mr2Completed := false, false
	for _, r := range r1 {
		if r.Status == "completed" {
			mr1Completed = true
		}
	}
	for _, r := range r2 {
		if r.Status == "completed" {
			mr2Completed = true
		}
	}
	if !mr1Completed {
		t.Errorf("MR 1 review did not complete")
	}
	if !mr2Completed {
		t.Errorf("MR 2 review did not complete")
	}
	if llm.RequestCount() != 2 {
		t.Errorf("expected 2 LLM calls (one per MR), got %d", llm.RequestCount())
	}

	notes := gitlab.Notes()
	if len(notes) != 2 {
		t.Errorf("expected 2 summary notes (one per MR), got %d", len(notes))
	}
	discussions := gitlab.Discussions()
	if len(discussions) != 4 {
		t.Errorf("expected 4 discussions (2 per MR), got %d", len(discussions))
	}

	// Verify discussions are posted to the correct MR endpoints
	mr1Discs, mr2Discs := 0, 0
	for _, d := range discussions {
		switch d.MRNumber {
		case "1":
			mr1Discs++
		case "2":
			mr2Discs++
		}
	}
	if mr1Discs != 2 {
		t.Errorf("expected 2 discussions for MR 1, got %d", mr1Discs)
	}
	if mr2Discs != 2 {
		t.Errorf("expected 2 discussions for MR 2, got %d", mr2Discs)
	}
}

// TestZeroInlineComments verifies a clean diff with no issues found completes successfully.
func TestZeroInlineComments(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1, "title": "Clean change", "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/clean", "target_branch": "main",
            "sha": "zero1111", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "z.go", "new_path": "z.go",
            "diff": "@@ -1,2 +1,3 @@\n package z\n+// clean code\n func Z() {}",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1, "head_commit_sha": "zero1111",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	// LLM returns a review with summary but no inline comments
	llm.ResponseFunc = func(reqBody []byte) (int, json.RawMessage) {
		return 200, json.RawMessage(`{
            "id": "chatcmpl-zero-1", "object": "chat.completion", "model": "test-model",
            "choices": [{
                "index": 0,
                "message": {
                    "role": "assistant",
                    "tool_calls": [{
                        "id": "call_zero", "type": "function",
                        "function": {
                            "name": "final_result",
                            "arguments": "{\"summary\":\"Looks good, no issues found.\",\"comments\":[]}"
                        }
                    }]
                },
                "finish_reason": "stop"
            }],
            "usage": {"prompt_tokens": 50, "completion_tokens": 10, "total_tokens": 60}
        }`)
	}

	_, repoID, _ := SetupProviderAndRepo(t, clients, gitlab)

	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{RepoId: repoID, MrNumber: 1}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id

	run := PollReviewRun(t, clients.Review, runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 60*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}
	if len(gitlab.Notes()) != 1 {
		t.Errorf("expected 1 summary note, got %d", len(gitlab.Notes()))
	}
	if len(gitlab.Discussions()) != 0 {
		t.Errorf("expected 0 discussions for clean diff, got %d", len(gitlab.Discussions()))
	}
	if len(run.Comments) != 0 {
		t.Errorf("expected 0 inline comments in completed run, got %d", len(run.Comments))
	}
}

// TestInvalidTokenAtReviewTime verifies that a GitLab 401 at review time results in a FAILED run.
func TestInvalidTokenAtReviewTime(t *testing.T) {
	// Configure mock to return 401 for all MR endpoints (simulates expired/invalid token)
	gitlab.SetMR("100", "1", &MRConfig{
		StatusCode: 401,
		Details:    json.RawMessage(`{"message":"401 Unauthorized"}`),
	})
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	_, repoID, _ := SetupProviderAndRepo(t, clients, gitlab)

	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{RepoId: repoID, MrNumber: 1}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id

	run := PollReviewRun(t, clients.Review, runID,
		apiv1.ReviewStatus_REVIEW_STATUS_FAILED, 60*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_FAILED {
		t.Errorf("expected FAILED (401 from GitLab), got %s", run.Status)
	}
	if llm.RequestCount() != 0 {
		t.Errorf("expected 0 LLM calls when GitLab returns 401, got %d", llm.RequestCount())
	}
	if len(gitlab.Notes()) != 0 {
		t.Errorf("expected 0 notes, got %d", len(gitlab.Notes()))
	}
}

// TestClosedMergedMRIgnored verifies that close and merge webhook actions produce no review run.
func TestClosedMergedMRIgnored(t *testing.T) {
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	// Webhook with action=close
	resp := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "close", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("close webhook: expected 200, got %d", resp.StatusCode)
	}

	// Webhook with action=merge
	resp2 := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "merge", "draft": false, "work_in_progress": false,
		},
	})
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("merge webhook: expected 200, got %d", resp2.StatusCode)
	}

	time.Sleep(3 * time.Second)
	runs := QueryReviewRuns(t, repoID, 1)
	if len(runs) != 0 {
		t.Errorf("expected 0 review runs for close/merge webhooks, got %d", len(runs))
	}
	if llm.RequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", llm.RequestCount())
	}
}

// TestDebounceRapidPushes verifies two rapid webhooks (different SHAs) collapse to one review.
// The second webhook cancels the first in-flight review; the resulting review debounces.
func TestDebounceRapidPushes(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1, "title": "Rapid push v1", "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/rapid", "target_branch": "main",
            "sha": "dbnc1sha0", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "d.go", "new_path": "d.go",
            "diff": "@@ -1,2 +1,3 @@\n package d\n+// debounce v1\n func D() {}",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1, "head_commit_sha": "dbnc1sha0",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	llm.DefaultResponse = defaultLLMResponse
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	// Send first webhook
	resp := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()

	// Immediately update to a new SHA and send second webhook (cancels first)
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1, "title": "Rapid push v2", "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/rapid", "target_branch": "main",
            "sha": "dbnc2sha0", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "d.go", "new_path": "d.go",
            "diff": "@@ -1,2 +1,3 @@\n package d\n+// debounce v2 wins\n func D() {}",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 2, "head_commit_sha": "dbnc2sha0",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	resp2 := SendWebhook(t, clients.BaseURL, providerID, webhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 100},
		"object_attributes": map[string]any{
			"iid": 1, "action": "update", "draft": false, "work_in_progress": false,
		},
	})
	resp2.Body.Close()

	// Wait for the single completed review (second, after debounce)
	WaitForReviewRun(t, repoID, 1, "completed", 60*time.Second)

	runs := QueryReviewRuns(t, repoID, 1)
	completedCount := 0
	for _, r := range runs {
		if r.Status == "completed" {
			completedCount++
		}
	}
	if completedCount != 1 {
		t.Errorf("expected 1 completed run (debounce collapsed rapid pushes), got %d", completedCount)
	}
	// First review was cancelled before reaching LLM; second calls LLM after debounce
	if llm.RequestCount() != 1 {
		t.Errorf("expected 1 LLM call (second review only), got %d", llm.RequestCount())
	}
}

// TestMalformedWebhookBody verifies a malformed JSON webhook body doesn't cause a 500 error.
func TestMalformedWebhookBody(t *testing.T) {
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	providerID, repoID, webhookSecret := SetupProviderAndRepo(t, clients, gitlab)

	// Send raw invalid JSON with a valid auth token
	body := []byte(`not valid json{{`)
	req, err := http.NewRequest("POST",
		fmt.Sprintf("%s/webhooks/%s", clients.BaseURL, providerID),
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", webhookSecret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sending malformed webhook: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode == 500 {
		t.Errorf("malformed webhook body: got 500, want 4xx or 200")
	}

	time.Sleep(2 * time.Second)
	runs := QueryReviewRuns(t, repoID, 1)
	if len(runs) != 0 {
		t.Errorf("expected 0 review runs for malformed webhook, got %d", len(runs))
	}
	if llm.RequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", llm.RequestCount())
	}
}

// TestManyInlineComments verifies 50 inline comments are all posted correctly to GitLab.
func TestManyInlineComments(t *testing.T) {
	gitlab.SetMR("100", "1", &MRConfig{
		Details: json.RawMessage(`{
            "iid": 1, "title": "Big PR", "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/big", "target_branch": "main",
            "sha": "many1111", "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{"old_path": "big.go", "new_path": "big.go",
            "diff": "@@ -1,2 +1,52 @@\n package big\n+// many issues here\n func Big() {}",
            "new_file": false, "deleted_file": false, "renamed_file": false}]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1, "head_commit_sha": "many1111",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})
	t.Cleanup(func() { gitlab.Reset(); llm.Reset() })
	gitlab.Reset()
	llm.Reset()

	// Build a response with 50 inline comments
	type comment struct {
		FilePath  string `json:"file_path"`
		LineStart int    `json:"line_start"`
		LineEnd   int    `json:"line_end"`
		Body      string `json:"body"`
	}
	type finalResult struct {
		Summary  string    `json:"summary"`
		Comments []comment `json:"comments"`
	}
	comments := make([]comment, 50)
	for i := range comments {
		comments[i] = comment{
			FilePath:  "big.go",
			LineStart: i + 1,
			LineEnd:   i + 1,
			Body:      fmt.Sprintf("Issue %d: consider refactoring this line.", i+1),
		}
	}
	resultArgs := finalResult{
		Summary:  "Found 50 issues requiring attention.",
		Comments: comments,
	}
	argsJSON, _ := json.Marshal(resultArgs)
	llmResp := map[string]any{
		"id": "chatcmpl-many-1", "object": "chat.completion", "model": "test-model",
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []map[string]any{{
					"id": "call_many", "type": "function",
					"function": map[string]any{
						"name": "final_result", "arguments": string(argsJSON),
					},
				}},
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{"prompt_tokens": 200, "completion_tokens": 500, "total_tokens": 700},
	}
	llmRespJSON, _ := json.Marshal(llmResp)
	llm.ResponseFunc = func(reqBody []byte) (int, json.RawMessage) {
		return 200, json.RawMessage(llmRespJSON)
	}

	_, repoID, _ := SetupProviderAndRepo(t, clients, gitlab)

	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{RepoId: repoID, MrNumber: 1}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id

	run := PollReviewRun(t, clients.Review, runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 120*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}
	if len(run.Comments) != 50 {
		t.Errorf("expected 50 comments in run, got %d", len(run.Comments))
	}
	discussions := gitlab.Discussions()
	if len(discussions) != 50 {
		t.Errorf("expected 50 discussions posted to GitLab, got %d", len(discussions))
	}
}
