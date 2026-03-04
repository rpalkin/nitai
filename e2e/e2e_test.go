//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"strings"
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
		{ID: 100, Name: "test-project", PathWithNamespace: "group/test-project", HTTPURLToRepo: "http://gitlab.example.com/group/test-project.git"},
	})

	// 3. Start Docker Compose stack
	t := &testMainT{}
	stack = StartStack(t, gitlab, llm)
	clients = stack.Clients

	// 4. Run tests
	code := m.Run()

	// 5. Teardown
	StopStack(t, stack)
	gitlab.Server.Close()
	llm.Server.Close()

	os.Exit(code)
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
	run := PollReviewRun(t, clients.Review, runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED,
		60*time.Second, 2*time.Second)

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

	t.Log("A12-A13: checking LLM request content")
	llmReqs := llm.Requests()
	if len(llmReqs) > 0 {
		body := string(llmReqs[0].Body)
		if !strings.Contains(body, "final_result") {
			t.Errorf("LLM request missing 'final_result' tool")
		}
		if !strings.Contains(body, "ProcessOrder") {
			t.Errorf("LLM request missing diff content 'ProcessOrder'")
		}
	}
}
