//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
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
// Note: This test is slow (~3–4 min) due to the Restate smart-debounce timer on the second invocation.
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

// TestSemanticSearch verifies the reviewer finds a function definition via semantic search
// when it's not in the diff and posts a comment about an argument mismatch.
// This test is currently skipped — it serves as an executable spec for the semantic search feature.
func TestSemanticSearch(t *testing.T) {
	t.Skip("semantic search not yet integrated into reviewer pipeline")

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
	llm.ResponseFunc = func(reqBody []byte) (int, json.RawMessage) {
		return 200, json.RawMessage(`{
            "id": "chatcmpl-sem-1",
            "object": "chat.completion",
            "model": "test-model",
            "choices": [{
                "index": 0,
                "message": {
                    "role": "assistant",
                    "tool_calls": [{
                        "id": "call_sem",
                        "type": "function",
                        "function": {
                            "name": "final_result",
                            "arguments": "{\"summary\":\"Wrong argument count.\",\"comments\":[{\"file_path\":\"cmd/main.go\",\"line_start\":10,\"line_end\":10,\"body\":\"mathutil.Foo requires 2 arguments (x int, y int) but is called with only 1\"}]}"
                        }
                    }]
                },
                "finish_reason": "stop"
            }],
            "usage": {"prompt_tokens": 200, "completion_tokens": 50, "total_tokens": 250}
        }`)
	}
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
	discussions := gitlab.Discussions()
	if len(discussions) != 1 {
		t.Fatalf("expected 1 inline discussion, got %d", len(discussions))
	}
	if discussions[0].Position.NewLine != 10 {
		t.Errorf("discussion new_line = %d, want 10", discussions[0].Position.NewLine)
	}
	if !strings.Contains(discussions[0].Body, "mathutil.Foo requires 2 arguments") {
		t.Errorf("discussion body missing expected content: %s", discussions[0].Body)
	}
	// The LLM request must contain the Foo definition (from Qdrant, not from the diff)
	llmReqs := llm.Requests()
	if len(llmReqs) == 0 {
		t.Fatal("expected at least 1 LLM request")
	}
	if !strings.Contains(string(llmReqs[0].Body), "func Foo") {
		t.Errorf("LLM request missing Foo definition (expected from Qdrant context)")
	}
}
