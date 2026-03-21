//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	apiv1 "ai-reviewer/gen/api/v1"
	"connectrpc.com/connect"
)

// TestInvalidWebhookSecret verifies webhook authentication rejects bad/missing secrets.
func TestInvalidWebhookSecret(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	payload := map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open", "draft": false, "work_in_progress": false,
		},
	}

	// Wrong secret
	resp := SendWebhook(t, clients.BaseURL, tc.ProviderID, "wrong-secret", payload)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("wrong secret: expected 401, got %d", resp.StatusCode)
	}

	// Empty secret
	resp2 := SendWebhook(t, clients.BaseURL, tc.ProviderID, "", payload)
	resp2.Body.Close()
	if resp2.StatusCode != 401 {
		t.Errorf("empty secret: expected 401, got %d", resp2.StatusCode)
	}

	time.Sleep(3 * time.Second)
	if tc.LLMRequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", tc.LLMRequestCount())
	}
}

// TestUnknownRepoWebhook verifies webhook for an unregistered project is accepted but produces no review.
func TestUnknownRepoWebhook(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Send webhook for an unknown project (ID=999)
	resp := SendWebhook(t, clients.BaseURL, tc.ProviderID, tc.WebhookSecret, map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": 999},
		"object_attributes": map[string]any{
			"action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	time.Sleep(3 * time.Second)
	if tc.LLMRequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", tc.LLMRequestCount())
	}
	runs := tc.QueryReviewRuns()
	if len(runs) != 0 {
		t.Errorf("expected 0 review runs for project 999, got %d", len(runs))
	}
}

// TestDraftMRNoReview verifies draft MRs are recorded but not reviewed.
func TestDraftMRNoReview(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
            "iid": 1, "title": "WIP: draft PR", "description": "",
            "author": {"username": "alice"},
            "source_branch": "feature/wip", "target_branch": "main",
            "sha": "draft111", "draft": true
        }`),
		Changes:  json.RawMessage(`{"changes":[]}`),
		Versions: json.RawMessage(`[]`),
	})

	resp := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open", "draft": true, "work_in_progress": true,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Draft run should appear in DB quickly (no Restate dispatch needed)
	tc.WaitForReviewRun("draft", 15*time.Second)

	if tc.LLMRequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", tc.LLMRequestCount())
	}
	if len(tc.Notes()) != 0 {
		t.Errorf("expected 0 notes, got %d", len(tc.Notes()))
	}
	if len(tc.Discussions()) != 0 {
		t.Errorf("expected 0 discussions, got %d", len(tc.Discussions()))
	}
}

// TestDraftToReadyTransition verifies unmarking a draft MR triggers a full review.
func TestDraftToReadyTransition(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// First webhook: draft MR (no review)
	tc.SetMR(&MRConfig{
		Details:  json.RawMessage(`{"title": "Add order processing", "description": "Implements order handler", "author": {"username": "alice"}, "source_branch": "` + fixtures.SimpleChange.SourceBranch + `", "target_branch": "` + fixtures.SimpleChange.TargetBranch + `", "sha": "` + fixtures.SimpleChange.HeadSHA + `", "draft": true}`),
		Changes:  fixtures.SimpleChange.Changes,
		Versions: fixtures.SimpleChange.Versions,
	})

	// Step 1: Send draft webhook
	resp := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open", "draft": true, "work_in_progress": true,
		},
	})
	resp.Body.Close()
	tc.WaitForReviewRun("draft", 15*time.Second)
	if tc.LLMRequestCount() != 0 {
		t.Errorf("expected 0 LLM calls after draft webhook, got %d", tc.LLMRequestCount())
	}

	// Step 2: Send draft→ready transition webhook (use SetMRFromBranch which sets draft=false)
	tc.SetMRFromBranch(fixtures.SimpleChange, "Add order processing", "Implements order handler", "alice")
	resp2 := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "update", "draft": false, "work_in_progress": false,
		},
		"changes": map[string]any{
			"draft": map[string]any{"previous": true, "current": false},
		},
	})
	resp2.Body.Close()

	// Wait for completed run
	dbRun := tc.WaitForReviewRun("completed", 90*time.Second)
	t.Logf("completed run id=%s", dbRun.ID)

	if tc.LLMRequestCount() != 1 {
		t.Errorf("expected 1 LLM call total, got %d", tc.LLMRequestCount())
	}
	notes := tc.Notes()
	if len(notes) == 0 {
		t.Errorf("expected at least 1 summary note posted")
	}
}

// TestReviewDisabledRepo verifies webhooks for repos with review disabled produce no run.
func TestReviewDisabledRepo(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	_, err := clients.Repo.DisableReview(context.Background(),
		connect.NewRequest(&apiv1.DisableReviewRequest{RepoId: tc.RepoID}))
	if err != nil {
		t.Fatalf("DisableReview: %v", err)
	}

	resp := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	time.Sleep(3 * time.Second)
	runs := tc.QueryReviewRuns()
	if len(runs) != 0 {
		t.Errorf("expected 0 review runs, got %d", len(runs))
	}
	if tc.LLMRequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", tc.LLMRequestCount())
	}
}

// TestDisableReviewStopsWebhook verifies that disabling review stops new webhook-triggered reviews.
func TestDisableReviewStopsWebhook(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	tc.SetMRFromBranch(fixtures.SimpleChange, "Fix bug", "", "alice")
	llm.DefaultResponse = defaultLLMResponse

	// First webhook → review completes
	resp := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("first webhook: expected 200, got %d", resp.StatusCode)
	}
	tc.WaitForReviewRun("completed", 90*time.Second)
	if tc.LLMRequestCount() != 1 {
		t.Errorf("expected 1 LLM call after first webhook, got %d", tc.LLMRequestCount())
	}

	// Record run count before disabling
	runsAfterFirst := tc.QueryReviewRuns()

	// Disable review
	_, err := clients.Repo.DisableReview(context.Background(),
		connect.NewRequest(&apiv1.DisableReviewRequest{RepoId: tc.RepoID}))
	if err != nil {
		t.Fatalf("DisableReview: %v", err)
	}

	// Second webhook — should be ignored since review is disabled
	resp2 := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "update", "draft": false, "work_in_progress": false,
		},
	})
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("webhook after disable: expected 200, got %d", resp2.StatusCode)
	}

	time.Sleep(3 * time.Second)
	if tc.LLMRequestCount() != 1 {
		t.Errorf("expected 1 total LLM call (second webhook ignored after disable), got %d", tc.LLMRequestCount())
	}
	runsAfterSecond := tc.QueryReviewRuns()
	if len(runsAfterSecond) != len(runsAfterFirst) {
		t.Errorf("expected no new review runs after disable, got %d extra",
			len(runsAfterSecond)-len(runsAfterFirst))
	}
}

// TestClosedMergedMRIgnored verifies that close and merge webhook actions produce no review run.
func TestClosedMergedMRIgnored(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Webhook with action=close
	resp := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "close", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("close webhook: expected 200, got %d", resp.StatusCode)
	}

	// Webhook with action=merge
	resp2 := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "merge", "draft": false, "work_in_progress": false,
		},
	})
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("merge webhook: expected 200, got %d", resp2.StatusCode)
	}

	time.Sleep(3 * time.Second)
	runs := tc.QueryReviewRuns()
	if len(runs) != 0 {
		t.Errorf("expected 0 review runs for close/merge webhooks, got %d", len(runs))
	}
	if tc.LLMRequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", tc.LLMRequestCount())
	}
}

// TestMalformedWebhookBody verifies a malformed JSON webhook body doesn't cause a 500 error.
func TestMalformedWebhookBody(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Send raw invalid JSON with a valid auth token
	body := []byte(`not valid json{{`)
	req, err := http.NewRequest("POST",
		fmt.Sprintf("%s/webhooks/%s", clients.BaseURL, tc.ProviderID),
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", tc.WebhookSecret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sending malformed webhook: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode == 500 {
		t.Errorf("malformed webhook body: got 500, want 4xx or 200")
	}

	time.Sleep(2 * time.Second)
	runs := tc.QueryReviewRuns()
	if len(runs) != 0 {
		t.Errorf("expected 0 review runs for malformed webhook, got %d", len(runs))
	}
	if tc.LLMRequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", tc.LLMRequestCount())
	}
}
