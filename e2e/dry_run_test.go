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

func TestDryRunReview(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	t.Log("--- Setup: configuring mock GitLab for MR with real git data ---")
	tc.SetMRFromBranch(fixtures.SimpleChange, "Add feature", "Test dry-run", "bob")
	llm.DefaultResponse = defaultLLMResponse

	t.Logf("--- Step 1: TriggerReview with dry_run=true (repoID=%s, MR=%s) ---", tc.RepoID, tc.MRIID)
	mrNum, _ := parseInt64(tc.MRIID)
	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{
			RepoId:   tc.RepoID,
			MrNumber: mrNum,
			DryRun:   true,
		}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id
	t.Logf("TriggerReview OK: runID=%s", runID)

	t.Log("--- Step 2: Polling until COMPLETED ---")
	run := tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED,
		90*time.Second, 2*time.Second)

	t.Log("--- Assertions ---")

	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}

	if !run.DryRun {
		t.Errorf("expected DryRun=true in response")
	}

	t.Logf("A2: review run has %d comments", len(run.Comments))
	if len(run.Comments) != 2 {
		t.Fatalf("expected 2 comments in DB, got %d", len(run.Comments))
	}

	notes := tc.Notes()
	t.Logf("A3: %d notes posted to GitLab (expect 0 for dry-run)", len(notes))
	if len(notes) != 0 {
		t.Errorf("dry-run should not post summary notes, got %d notes", len(notes))
		for i, note := range notes {
			t.Logf("  note[%d]: %s", i, note.Body)
		}
	}

	discussions := tc.Discussions()
	t.Logf("A4: %d discussions posted to GitLab (expect 0 for dry-run)", len(discussions))
	if len(discussions) != 0 {
		t.Errorf("dry-run should not post inline discussions, got %d discussions", len(discussions))
		for i, d := range discussions {
			t.Logf("  disc[%d]: %s at %s:%d", i, d.Body, d.Position.NewPath, d.Position.NewLine)
		}
	}

	if tc.LLMRequestCount() != 1 {
		t.Errorf("expected 1 LLM request (review should run), got %d", tc.LLMRequestCount())
	}

	llmReqs := tc.LLMRequests()
	if len(llmReqs) > 0 {
		body := string(llmReqs[0].Body)
		if !strings.Contains(body, "final_result") {
			t.Errorf("LLM request missing 'final_result' tool")
		}
	}

	dbRuns := tc.QueryReviewRuns()
	if len(dbRuns) != 1 {
		t.Fatalf("expected 1 review run in DB, got %d", len(dbRuns))
	}

	t.Logf("✓ Dry-run review completed successfully with no comments posted to provider")
}
