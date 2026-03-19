//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	apiv1 "ai-reviewer/gen/api/v1"
	"connectrpc.com/connect"
)

// TestLargeDiffShortCircuit verifies diffs >5000 lines skip LLM and post a canned message.
func TestLargeDiffShortCircuit(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Generate a diff with 5001 changed lines (each starts with '+')
	var sb strings.Builder
	sb.WriteString("@@ -1,0 +1,5001 @@\n")
	for i := 0; i < 5001; i++ {
		fmt.Fprintf(&sb, "+line%d\n", i)
	}
	largeDiff := sb.String()

	tc.SetMR(&MRConfig{
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

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	run := tc.PollReviewRun(runID, apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 60*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}
	if tc.LLMRequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", tc.LLMRequestCount())
	}
	notes := tc.Notes()
	if len(notes) != 1 {
		t.Fatalf("expected 1 note, got %d", len(notes))
	}
	if !strings.Contains(strings.ToLower(notes[0].Body), "too large") {
		t.Errorf("note body missing 'too large': %s", notes[0].Body)
	}
	if len(tc.Discussions()) != 0 {
		t.Errorf("expected 0 discussions, got %d", len(tc.Discussions()))
	}
}

// TestProviderDeletionCascade verifies deleting a provider soft-deletes repos and rejects future webhooks.
func TestProviderDeletionCascade(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Delete provider
	_, err := clients.Provider.DeleteProvider(context.Background(),
		connect.NewRequest(&apiv1.DeleteProviderRequest{Id: tc.ProviderID}))
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
		if p.Id == tc.ProviderID {
			t.Errorf("deleted provider still in list")
		}
	}

	// Repos for deleted provider should be empty
	listReposResp, err := clients.Repo.ListRepos(context.Background(),
		connect.NewRequest(&apiv1.ListReposRequest{ProviderId: tc.ProviderID}))
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(listReposResp.Msg.Repositories) != 0 {
		t.Errorf("expected 0 repos for deleted provider, got %d", len(listReposResp.Msg.Repositories))
	}

	// Webhook for deleted provider should return 404 (provider not found)
	resp := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("expected 404 for deleted provider webhook, got %d", resp.StatusCode)
	}

	time.Sleep(2 * time.Second)
	runs := tc.QueryReviewRuns()
	if len(runs) != 0 {
		t.Errorf("expected 0 review runs after provider deletion, got %d", len(runs))
	}
	if tc.LLMRequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", tc.LLMRequestCount())
	}
}

// TestLLMTerminalError verifies LLM HTTP 400 errors result in a FAILED review run.
func TestLLMTerminalError(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
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

	// Set custom response function via TestContext
	tc.SetResponseFunc(func(reqBody []byte) (int, json.RawMessage) {
		return 400, json.RawMessage(`{"error":{"message":"invalid_request","type":"invalid_request_error"}}`)
	})

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	run := tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_FAILED, 120*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_FAILED {
		t.Errorf("expected FAILED, got %s", run.Status)
	}
	if len(tc.Notes()) != 0 {
		t.Errorf("expected 0 notes, got %d", len(tc.Notes()))
	}
	if len(tc.Discussions()) != 0 {
		t.Errorf("expected 0 discussions, got %d", len(tc.Discussions()))
	}
}

// TestGitLab404ForMR verifies GitLab returning 404 for MR fetch results in a FAILED review.
func TestGitLab404ForMR(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	tc.SetMR(&MRConfig{
		StatusCode: 404,
		Details:    json.RawMessage(`{"message":"404 Not found"}`),
	})
	llm.DefaultResponse = defaultLLMResponse

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	run := tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_FAILED, 60*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_FAILED {
		t.Errorf("expected FAILED, got %s", run.Status)
	}
	if tc.LLMRequestCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", tc.LLMRequestCount())
	}
	if len(tc.Notes()) != 0 {
		t.Errorf("expected 0 notes, got %d", len(tc.Notes()))
	}
}

// TestInvalidTokenAtReviewTime verifies that a GitLab 401 at review time results in a FAILED run.
func TestInvalidTokenAtReviewTime(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Configure mock to return 401 for all MR endpoints (simulates expired/invalid token)
	tc.SetMR(&MRConfig{
		StatusCode: 401,
		Details:    json.RawMessage(`{"message":"401 Unauthorized"}`),
	})

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	run := tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_FAILED, 60*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_FAILED {
		t.Errorf("expected FAILED (401 from GitLab), got %s", run.Status)
	}
	if tc.LLMRequestCount() != 0 {
		t.Errorf("expected 0 LLM calls when GitLab returns 401, got %d", tc.LLMRequestCount())
	}
	if len(tc.Notes()) != 0 {
		t.Errorf("expected 0 notes, got %d", len(tc.Notes()))
	}
}
