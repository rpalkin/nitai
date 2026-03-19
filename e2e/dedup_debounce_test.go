//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// TestDuplicateDiffDedup verifies the same diff hash sent twice produces only one review.
func TestDuplicateDiffDedup(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
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

	webhookPayload := map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open", "draft": false, "work_in_progress": false,
		},
	}

	// First webhook — wait for it to complete before sending second
	t.Log("sending first webhook, waiting for completion...")
	resp := tc.SendWebhook(webhookPayload)
	resp.Body.Close()
	tc.WaitForReviewRun("completed", 90*time.Second)
	if tc.LLMRequestCount() != 1 {
		t.Errorf("expected 1 LLM call after first webhook, got %d", tc.LLMRequestCount())
	}

	// Second identical webhook — same sha, dedup should mark run as skipped
	// Smart debounce may add ~3 min delay before the second invocation processes
	t.Log("sending second webhook (same sha), waiting for dedup skipped status...")
	resp2 := tc.SendWebhook(webhookPayload)
	resp2.Body.Close()
	tc.WaitForReviewRun("skipped", 240*time.Second)

	if tc.LLMRequestCount() != 1 {
		t.Errorf("expected 1 total LLM call (second was deduped), got %d", tc.LLMRequestCount())
	}
}

// TestReReviewOnNewPush verifies a new push to an existing MR triggers a fresh review.
func TestReReviewOnNewPush(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
            "title": "Feature branch", "description": "",
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

	// First webhook
	resp := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()
	tc.WaitForReviewRun("completed", 90*time.Second)
	if tc.LLMRequestCount() != 1 {
		t.Errorf("expected 1 LLM call after first push, got %d", tc.LLMRequestCount())
	}

	// Update MR to a new SHA (simulates new push with different changes)
	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
            "title": "Feature branch", "description": "",
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
	resp2 := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "update", "draft": false, "work_in_progress": false,
		},
	})
	resp2.Body.Close()

	// Wait for second completed run (no debounce since first completed normally)
	t.Log("waiting for second completed run...")
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		runs := tc.QueryReviewRuns()
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

	runs := tc.QueryReviewRuns()
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
	if tc.LLMRequestCount() != 2 {
		t.Errorf("expected 2 LLM calls (one per push), got %d", tc.LLMRequestCount())
	}
}

// TestCancelOnNewPush verifies that a new webhook while a review is in-flight cancels the first
// and only the second review completes (after debounce).
func TestCancelOnNewPush(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
            "title": "Cancel test v1", "description": "",
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

	// Block the first LLM response so the first review is in-flight when the second webhook arrives.
	// The second webhook triggers Restate to cancel the first invocation and start a new one.
	// After cancellation is triggered, we unblock the first LLM call - it should return normally,
	// but the Reviewer should stop processing because Restate closed the connection.
	unblockFirstLLM := make(chan struct{})
	firstCallReturned := make(chan struct{})
	var callCount atomic.Int32
	tc.SetResponseFunc(func(reqBody []byte) (int, json.RawMessage) {
		n := callCount.Add(1)
		if n == 1 {
			// First call: block until we're ready to proceed
			<-unblockFirstLLM
			close(firstCallReturned)
		}
		// Always return normal 200 response - LLM doesn't know about cancellation
		return 200, defaultLLMResponse
	})

	// Send first webhook
	resp := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()

	// Poll until first LLM call received (first review is now in-flight, blocked at LLM)
	t.Log("waiting for first LLM call (first review in-flight)...")
	pollDeadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(pollDeadline) {
		if tc.LLMRequestCount() >= 1 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if tc.LLMRequestCount() < 1 {
		close(unblockFirstLLM)
		t.Fatal("first LLM call not received within 60s")
	}
	t.Log("first LLM call received; sending second webhook to trigger Restate cancellation")

	// Update MR to new SHA and send second webhook (cancels first in-flight review)
	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
            "title": "Cancel test v2", "description": "",
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
	resp2 := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "update", "draft": false, "work_in_progress": false,
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
	tc.WaitForReviewRun("completed", 60*time.Second)

	// Only one review should have completed successfully.
	// The first invocation was cancelled by Restate and should not complete.
	runs := tc.QueryReviewRuns()
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
	if tc.LLMRequestCount() < 2 {
		t.Errorf("expected at least 2 LLM calls (first cancelled + second completed), got %d", tc.LLMRequestCount())
	}
}

// TestDebounceRapidPushes verifies two rapid webhooks (different SHAs) collapse to one review.
// The second webhook cancels the first in-flight review; the resulting review debounces.
func TestDebounceRapidPushes(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
            "title": "Rapid push v1", "description": "",
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

	// Send first webhook
	resp := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp.Body.Close()

	// Immediately update to a new SHA and send second webhook (cancels first)
	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
            "title": "Rapid push v2", "description": "",
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
	resp2 := tc.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "update", "draft": false, "work_in_progress": false,
		},
	})
	resp2.Body.Close()

	// Wait for the single completed review (second, after debounce)
	tc.WaitForReviewRun("completed", 60*time.Second)

	runs := tc.QueryReviewRuns()
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
	if tc.LLMRequestCount() != 1 {
		t.Errorf("expected 1 LLM call (second review only), got %d", tc.LLMRequestCount())
	}
}

// TestConcurrentMRReviews verifies two different MRs on the same repo are reviewed independently.
func TestConcurrentMRReviews(t *testing.T) {
	t.Parallel()
	tc1 := NewTestContext(t)

	// Create a second test context for the second MR
	mrIID2 := nextMRIID()
	tc2 := &TestContext{
		T:             t,
		MRIID:         mrIID2,
		Marker:        fmt.Sprintf("e2e-marker-%s", mrIID2),
		ProviderID:    tc1.ProviderID,
		RepoID:        tc1.RepoID,
		WebhookSecret: tc1.WebhookSecret,
		ProjectID:     "100",
	}
	// Register marker for second context
	llm.RegisterMarker(tc2.Marker)
	t.Cleanup(func() {
		llm.UnregisterMarker(tc2.Marker)
		gitlab.ResetForMR(tc2.ProjectID, tc2.MRIID)
	})

	// Set up first MR
	tc1.SetMR(&MRConfig{
		Details: json.RawMessage(`{
            "title": "Feature A", "description": "",
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

	// Set up second MR
	tc2.SetMR(&MRConfig{
		Details: json.RawMessage(`{
            "title": "Feature B", "description": "",
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

	// Send webhooks for both MRs near-simultaneously
	resp1 := tc1.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open", "draft": false, "work_in_progress": false,
		},
	})
	resp1.Body.Close()
	resp2 := tc2.SendWebhook(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open", "draft": false, "work_in_progress": false,
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
		r1 := tc1.QueryReviewRuns()
		r2 := tc2.QueryReviewRuns()
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

	r1 := tc1.QueryReviewRuns()
	r2 := tc2.QueryReviewRuns()
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

	totalLLMCalls := tc1.LLMRequestCount() + tc2.LLMRequestCount()
	if totalLLMCalls != 2 {
		t.Errorf("expected 2 LLM calls (one per MR), got %d", totalLLMCalls)
	}

	notes1 := tc1.Notes()
	notes2 := tc2.Notes()
	if len(notes1)+len(notes2) != 2 {
		t.Errorf("expected 2 summary notes (one per MR), got %d", len(notes1)+len(notes2))
	}
	discussions1 := tc1.Discussions()
	discussions2 := tc2.Discussions()
	if len(discussions1)+len(discussions2) != 4 {
		t.Errorf("expected 4 discussions (2 per MR), got %d", len(discussions1)+len(discussions2))
	}
}
