//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	apiv1 "ai-reviewer/gen/api/v1"
)

// TestZeroInlineComments verifies a clean diff with no issues found completes successfully.
func TestZeroInlineComments(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
            "title": "Clean change", "description": "",
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

	// LLM returns a review with summary but no inline comments
	tc.SetResponseFunc(func(reqBody []byte) (int, json.RawMessage) {
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
	})

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	run := tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 60*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}
	if len(tc.Notes()) != 1 {
		t.Errorf("expected 1 summary note, got %d", len(tc.Notes()))
	}
	if len(tc.Discussions()) != 0 {
		t.Errorf("expected 0 discussions for clean diff, got %d", len(tc.Discussions()))
	}
	if len(run.Comments) != 0 {
		t.Errorf("expected 0 inline comments in completed run, got %d", len(run.Comments))
	}
}

// TestManyInlineComments verifies 50 inline comments are all posted correctly to GitLab.
func TestManyInlineComments(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
            "title": "Big PR", "description": "",
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
	tc.SetResponseFunc(func(reqBody []byte) (int, json.RawMessage) {
		return 200, json.RawMessage(llmRespJSON)
	})

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	run := tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 120*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}
	if len(run.Comments) != 50 {
		t.Errorf("expected 50 comments in run, got %d", len(run.Comments))
	}
	discussions := tc.Discussions()
	if len(discussions) != 50 {
		t.Errorf("expected 50 discussions posted to GitLab, got %d", len(discussions))
	}
}
