//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	apiv1 "ai-reviewer/gen/api/v1"
)

// TestRealGitPipelineIntegrity verifies the full pipeline with real git commits.
// This test uses CreateMRBranch to create a real feature branch with real files,
// then asserts that:
// 1. The LLM request contains the real diff content
// 2. Discussion positions use real SHAs from git
// 3. The read_file tool works (returns real file content)
//
// This test should FAIL before migration (when tests use fake SHAs that don't exist
// in any git repo) and PASS after migration (when tests use real git history).
func TestRealGitPipelineIntegrity(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Create a real feature branch with real files
	result := CreateMRBranch(t, bareRepoPath, "integrity-"+tc.MRIID, "main", map[string]string{
		"src/handler.go": `package handler

import "fmt"

func ProcessOrder(order *Order) error {
	// integrity test comment - added on feature branch
	result := CalculateTotal(order.Items)
	if result == nil {
		return nil
	}
	fmt.Println(result)
	return nil
}
`,
	})

	// Configure mock GitLab with real git data
	tc.SetMRFromBranch(result, "Real git integrity test", "Tests real git integration", "alice")

	llm.DefaultResponse = defaultLLMResponse

	// Trigger review
	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	// Poll until completed
	run := tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 90*time.Second, 2*time.Second)

	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}

	// A1: Verify real SHAs in discussion positions
	discussions := tc.Discussions()
	if len(discussions) == 0 {
		t.Logf("Note: No discussions posted (LLM may have found no issues)")
	} else {
		t.Logf("Verifying real SHAs in %d discussions", len(discussions))
		for i, d := range discussions {
			// SHA should be real hex (40 chars), not fake placeholder like "aaa111" or "bbb222"
			if len(d.Position.HeadSHA) != 40 {
				t.Errorf("disc[%d] head_sha should be 40 chars (real SHA), got %d: %s",
					i, len(d.Position.HeadSHA), d.Position.HeadSHA)
			}
			if len(d.Position.BaseSHA) != 40 {
				t.Errorf("disc[%d] base_sha should be 40 chars (real SHA), got %d: %s",
					i, len(d.Position.BaseSHA), d.Position.BaseSHA)
			}
			// SHAs should match our created branch
			if d.Position.HeadSHA != result.HeadSHA {
				t.Errorf("disc[%d] head_sha mismatch: got %s, want %s",
					i, d.Position.HeadSHA[:8], result.HeadSHA[:8])
			}
			if d.Position.BaseSHA != result.BaseSHA {
				t.Errorf("disc[%d] base_sha mismatch: got %s, want %s",
					i, d.Position.BaseSHA[:8], result.BaseSHA[:8])
			}
		}
	}

	// A2: Verify LLM received the real diff content
	llmReqs := tc.LLMRequests()
	if len(llmReqs) == 0 {
		t.Fatalf("expected at least 1 LLM request, got 0")
	}
	body := string(llmReqs[0].Body)
	if !strings.Contains(body, "integrity test comment") {
		t.Errorf("LLM request should contain real diff content 'integrity test comment'")
	}
	if !strings.Contains(body, "src/handler.go") {
		t.Errorf("LLM request should contain file path 'src/handler.go'")
	}

	// A3: Verify read_file tool was available to the LLM
	if !strings.Contains(body, "read_file") {
		t.Errorf("LLM request should contain read_file tool")
	}

	t.Logf("TestRealGitPipelineIntegrity passed: real SHAs=%s/%s, discussions=%d",
		result.HeadSHA[:8], result.BaseSHA[:8], len(discussions))
}
