//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	apiv1 "ai-reviewer/gen/api/v1"
	"connectrpc.com/connect"
)

func TestFullPipelineViaTriggerReview(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	t.Log("--- Setup: configuring mock GitLab for MR with real git data ---")
	tc.SetMRFromBranch(fixtures.SimpleChange, "Add order processing", "Implements order handler", "alice")
	llm.DefaultResponse = defaultLLMResponse

	t.Logf("--- Step 4: TriggerReview (repoID=%s, MR=%s) ---", tc.RepoID, tc.MRIID)
	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	t.Logf("TriggerReview OK: runID=%s", runID)

	t.Log("--- Step 5: Polling until COMPLETED ---")
	// After 2.9: pipeline includes SyncRepo + IndexRepo; allow extra time.
	run := tc.PollReviewRun(runID,
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

	// A8: Summary note posted
	notes := tc.Notes()
	t.Logf("A8: %d summary notes posted", len(notes))
	if len(notes) != 1 {
		t.Fatalf("expected 1 posted note, got %d", len(notes))
	}
	if !strings.Contains(notes[0].Body, "nil pointer") {
		t.Errorf("summary note missing 'nil pointer': %s", notes[0].Body)
	}

	// A9: Inline discussions posted
	discussions := tc.Discussions()
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
		if d.Position.BaseSHA != fixtures.SimpleChange.BaseSHA {
			t.Errorf("disc[%d] base_sha = %q, want %q", i, d.Position.BaseSHA, fixtures.SimpleChange.BaseSHA)
		}
		if d.Position.HeadSHA != fixtures.SimpleChange.HeadSHA {
			t.Errorf("disc[%d] head_sha = %q, want %q", i, d.Position.HeadSHA, fixtures.SimpleChange.HeadSHA)
		}
		if d.Position.StartSHA != fixtures.SimpleChange.BaseSHA {
			t.Errorf("disc[%d] start_sha = %q, want %q", i, d.Position.StartSHA, fixtures.SimpleChange.BaseSHA)
		}
	}

	// A11: LLM request count
	t.Logf("A11: %d LLM requests made", tc.LLMRequestCount())
	if tc.LLMRequestCount() != 1 {
		t.Errorf("expected 1 LLM request, got %d", tc.LLMRequestCount())
	}

	t.Log("A12-A14: checking LLM request content")
	llmReqs := tc.LLMRequests()
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
	t.Parallel()
	tc := NewTestContext(t)

	t.Log("--- Setup: configuring mock GitLab for MR with real git data ---")
	tc.SetMRFromBranch(fixtures.SimpleChange, "Add order processing", "Implements order handler", "alice")
	llm.DefaultResponse = defaultLLMResponse

	t.Log("--- Sending webhook for MR, action=open ---")
	resp := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("webhook: expected 200, got %d", resp.StatusCode)
	}

	t.Log("--- Waiting for completed review run in DB ---")
	dbRun := tc.WaitForReviewRun("completed", 90*time.Second)
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
	notes := tc.Notes()
	t.Logf("A8: %d summary notes posted", len(notes))
	if len(notes) != 1 {
		t.Fatalf("expected 1 posted note, got %d", len(notes))
	}
	if !strings.Contains(notes[0].Body, "nil pointer") {
		t.Errorf("summary note missing 'nil pointer': %s", notes[0].Body)
	}

	// A9: Inline discussions posted
	discussions := tc.Discussions()
	t.Logf("A9: %d inline discussions posted", len(discussions))
	if len(discussions) != 2 {
		t.Fatalf("expected 2 posted discussions, got %d", len(discussions))
	}

	// A11: LLM called once
	if tc.LLMRequestCount() != 1 {
		t.Errorf("expected 1 LLM request, got %d", tc.LLMRequestCount())
	}
}

// TestSingleRunPerWebhookReview verifies that webhook-triggered reviews create exactly ONE run
// with the correct restate_invocation_id set (regression test for double-run bug).
func TestSingleRunPerWebhookReview(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	tc.SetMRFromBranch(fixtures.NewFile, "Single run test", "", "alice")
	llm.DefaultResponse = defaultLLMResponse

	// Send webhook
	resp := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()

	// Wait for review to complete
	tc.WaitForReviewRun("completed", 120*time.Second)

	// Verify only ONE run was created
	runs := tc.QueryReviewRuns()
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
