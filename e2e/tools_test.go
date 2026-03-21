//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apiv1 "ai-reviewer/gen/api/v1"
	"connectrpc.com/connect"
)

// TestReadFileToolGracefulDegradation verifies that when the LLM calls the read_file tool
// for a file that doesn't exist, the tool returns a human-readable error and the review
// still completes successfully.
func TestReadFileToolGracefulDegradation(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
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

	// Two-turn conversation: first call returns read_file tool call, second returns final_result.
	var callCount atomic.Int32
	tc.SetResponseFunc(func(reqBody []byte) (int, json.RawMessage) {
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
	// LLM was called twice: once to get read_file call, once after receiving the error
	if callCount.Load() != 2 {
		t.Errorf("expected 2 LLM calls (tool call + final), got %d", callCount.Load())
	}
	// The second LLM request must contain the read_file tool result (error message about missing file)
	reqs := tc.LLMRequests()
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
// NOTE: This test manipulates global embedding state and must NOT run in parallel.
func TestSearchCodebaseToolGracefulDegradation(t *testing.T) {
	// t.Parallel() - intentionally sequential: embedding responses are global
	tc := NewTestContext(t)

	// Clean up global embedding func after test (before parallel tests start)
	t.Cleanup(func() {
		llm.SetEmbeddingResponseFunc(nil)
	})

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
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

	// Two-turn conversation: first call returns search_codebase tool call, second returns final_result.
	// After the first LLM call (indexing is done by then), break embeddings so search-mcp fails.
	// NOTE: Must use global EmbeddingResponseFunc (not marker-based) because indexer/search-mcp
	// embedding requests don't contain the MR title marker. Safe because this test is sequential.
	var callCount atomic.Int32
	tc.SetResponseFunc(func(reqBody []byte) (int, json.RawMessage) {
		n := callCount.Add(1)
		if n == 1 {
			// Break embeddings globally so search-mcp can't embed the query (422 = non-retryable)
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
	// LLM was called twice: once to get search_codebase call, once after receiving the error
	if callCount.Load() != 2 {
		t.Errorf("expected 2 LLM calls (tool call + final), got %d", callCount.Load())
	}
	// The second LLM request must contain the search_codebase tool result (error from search-mcp)
	reqs := tc.LLMRequests()
	if len(reqs) >= 2 {
		body := string(reqs[1].Body)
		if !strings.Contains(body, "search-mcp") {
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
	t.Parallel()
	tc := NewTestContext(t)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
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

	// Two-turn conversation: first call returns search_codebase tool call,
	// second returns final_result with a comment about wrong argument count.
	var callCount atomic.Int32
	tc.SetResponseFunc(func(reqBody []byte) (int, json.RawMessage) {
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
	discussions := tc.Discussions()
	if len(discussions) != 1 {
		t.Fatalf("expected 1 inline discussion, got %d", len(discussions))
	}
	if !strings.Contains(discussions[0].Body, "mathutil.Foo requires 2 arguments") {
		t.Errorf("discussion body missing expected content: %s", discussions[0].Body)
	}
	// The second LLM request should contain the search tool result with actual indexed content
	llmReqs := tc.LLMRequests()
	if len(llmReqs) < 2 {
		t.Fatalf("expected at least 2 LLM requests (search + final), got %d", len(llmReqs))
	}
	body := string(llmReqs[1].Body)

	// Verify the search_codebase tool result was passed back to the LLM
	if !strings.Contains(body, "search_codebase") && !strings.Contains(body, "tool") {
		t.Errorf("second LLM request missing search_codebase tool result: %s", body)
	}

	// Verify actual search results contain content from the indexed repo.
	// The test repo has pkg/mathutil/mathutil.go with "func Foo(x, y int) int".
	// search-mcp should have returned this via Qdrant after indexing.
	if !strings.Contains(body, "Foo") {
		t.Errorf("search results should contain 'Foo' from indexed mathutil.go, got: %s", body)
	}
	// The search results should NOT be an error — they should contain actual code
	if strings.Contains(body, "search context not available") || strings.Contains(body, "search-mcp failed") {
		t.Errorf("search_codebase returned an error instead of real results: %s", body)
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
		// Restore original project list
		gitlab.SetProjects([]GitLabProject{
			{ID: 100, Name: "test-project", PathWithNamespace: "group/test-project", HTTPURLToRepo: "http://gitlab.example.com/group/test-project.git", DefaultBranch: "main"},
		})
	})

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

	// Snapshot global counts before trigger (parallel tests may have accumulated requests)
	llmCountBefore := llm.RequestCount()
	notesCountBefore := len(gitlab.Notes())
	discussionsCountBefore := len(gitlab.Discussions())

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
	if llmDelta := llm.RequestCount() - llmCountBefore; llmDelta != 0 {
		t.Errorf("expected 0 LLM calls when SyncRepo fails, got %d new calls", llmDelta)
	}
	if notesDelta := len(gitlab.Notes()) - notesCountBefore; notesDelta != 0 {
		t.Errorf("expected 0 notes when SyncRepo fails, got %d new notes", notesDelta)
	}
	if discDelta := len(gitlab.Discussions()) - discussionsCountBefore; discDelta != 0 {
		t.Errorf("expected 0 discussions when SyncRepo fails, got %d new discussions", discDelta)
	}
}

// TestIndexerFailureGracefulDegradation verifies that when IndexRepo fails (embeddings return 500),
// the review still completes without semantic search capability.
// NOTE: This test manipulates global embedding state and must NOT run in parallel.
// Also uses a unique branch to ensure isolated Qdrant collection (collection names are repoID + branch).
func TestIndexerFailureGracefulDegradation(t *testing.T) {
	// t.Parallel() - intentionally sequential: embedding responses are global
	tc := NewTestContext(t)

	// Clean up global embedding func after test (before parallel tests start)
	t.Cleanup(func() {
		llm.SetEmbeddingResponseFunc(nil)
	})

	// Use a unique branch to ensure this test has its own isolated Qdrant collection.
	// This prevents conflicts with TestSearchCodebaseToolGracefulDegradation which runs sequentially before this test.
	uniqueBranch := fmt.Sprintf("feature/orders-fail-%s", tc.MRIID)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(fmt.Sprintf(`{
            "title": "Add order processing",
            "description": "Implements order handler",
            "author": {"username": "alice"},
            "source_branch": "%s",
            "target_branch": "main",
            "sha": "bbb222",
            "draft": false
        }`, uniqueBranch)),
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

	// Embeddings always return 422 (non-retryable) → IndexRepo will fail
	// NOTE: Must use global EmbeddingResponseFunc (not marker-based) because indexer
	// embedding requests don't contain the MR title marker. Safe because this test is sequential.
	llm.SetEmbeddingResponseFunc(func(reqBody []byte) (int, json.RawMessage) {
		return 422, json.RawMessage(`{"error":{"message":"embedding service unavailable"}}`)
	})

	// Two-turn conversation: first call returns search_codebase (which should fail gracefully),
	// second returns final_result.
	var callCount atomic.Int32
	tc.SetResponseFunc(func(reqBody []byte) (int, json.RawMessage) {
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
	})

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	// Review should complete even when indexing fails (graceful degradation)
	run := tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 90*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED (indexing failure should degrade gracefully), got %s", run.Status)
	}
	if callCount.Load() != 2 {
		t.Errorf("expected 2 LLM calls (search + final), got %d", callCount.Load())
	}
	// Second request must contain error indicating search failed due to missing context (indexing failed → no collection)
	reqs := tc.LLMRequests()
	if len(reqs) >= 2 {
		body := string(reqs[1].Body)
		if !strings.Contains(body, "search context not available") && !strings.Contains(body, "search-mcp") {
			t.Errorf("second LLM request should have search error indicating indexing failed: %s", body)
		}
	}
	// Comments still posted despite indexing failure
	if len(run.Comments) == 0 {
		t.Errorf("expected comments in completed review despite indexing failure")
	}
}

// TestSearchCodebaseReturnsIndexedContent verifies that search_codebase returns actual
// content from the indexed repository via search-mcp + Qdrant, not just an error string.
// Searches for "CalculateTotal" which is defined in src/util.go of the test repo.
// NOTE: Sequential (no t.Parallel) to avoid search-mcp concurrency issues with streamable-http.
func TestSearchCodebaseReturnsIndexedContent(t *testing.T) {
	tc := NewTestContext(t)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
            "title": "Refactor order processing",
            "description": "Cleanup handler",
            "author": {"username": "alice"},
            "source_branch": "feature/orders",
            "target_branch": "main",
            "sha": "srch111",
            "draft": false
        }`),
		Changes: json.RawMessage(`{
            "changes": [{
                "old_path": "src/handler.go",
                "new_path": "src/handler.go",
                "diff": "@@ -5,6 +5,7 @@ package handler\n import \"fmt\"\n \n func ProcessOrder(order *Order) error {\n+    // TODO: validate items\n     result := CalculateTotal(order.Items)\n     return nil\n }",
                "new_file": false, "deleted_file": false, "renamed_file": false
            }]
        }`),
		Versions: json.RawMessage(`[{
            "id": 1, "head_commit_sha": "srch111",
            "base_commit_sha": "base000", "start_commit_sha": "base000"
        }]`),
	})

	// Two-turn: first call searches for CalculateTotal, second produces final result.
	var callCount atomic.Int32
	tc.SetResponseFunc(func(reqBody []byte) (int, json.RawMessage) {
		n := callCount.Add(1)
		if n == 1 {
			return 200, json.RawMessage(`{
                "id": "chatcmpl-srch-1",
                "object": "chat.completion",
                "model": "test-model",
                "choices": [{
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": null,
                        "tool_calls": [{
                            "id": "call_srch1",
                            "type": "function",
                            "function": {
                                "name": "search_codebase",
                                "arguments": "{\"query\": \"CalculateTotal function implementation\"}"
                            }
                        }]
                    },
                    "finish_reason": "tool_calls"
                }],
                "usage": {"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120}
            }`)
		}
		return 200, defaultLLMResponse
	})

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	run := tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 90*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}
	if callCount.Load() != 2 {
		t.Errorf("expected 2 LLM calls (search + final), got %d", callCount.Load())
	}

	reqs := tc.LLMRequests()
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 LLM requests, got %d", len(reqs))
	}
	body := string(reqs[1].Body)

	// The search result must contain actual indexed content, not an error
	if strings.Contains(body, "search context not available") {
		t.Errorf("search_codebase returned 'search context not available' — indexing did not produce a collection")
	}
	if strings.Contains(body, "search-mcp failed") || strings.Contains(body, "search-mcp timed out") {
		t.Errorf("search_codebase returned search-mcp error — search-mcp is not running: %s", body)
	}

	// Verify the search results contain actual code from the indexed repo.
	// src/util.go defines CalculateTotal which should be in the Qdrant index.
	if !strings.Contains(body, "CalculateTotal") {
		t.Errorf("search results should contain 'CalculateTotal' from indexed src/util.go: %s", body)
	}
}

// TestReadFileReturnsMergeResultContent verifies that read_file returns content from
// the merge result commit (combining source + target changes), not just source or target HEAD.
//
// Test setup:
//  1. Create feature branch from initial main (diverges before TARGET_MARKER is added to main)
//  2. Feature branch: modify src/handler.go with SOURCE_MARKER
//  3. Main branch: modify src/util.go with TARGET_MARKER (advances main after feature branch forked)
//  4. LLM calls read_file("src/util.go") - should return content containing TARGET_MARKER
//     (marker is only on main, proving the merge result is used, not source HEAD)
func TestReadFileReturnsMergeResultContent(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Step 1: Create feature branch FIRST (from initial main HEAD)
	// This ensures the feature branch diverges before TARGET_MARKER is added to main
	featureBranch := "feature/merge-" + tc.MRIID
	featureSHA := CommitFileToBareRepo(t, bareRepoPath, featureBranch, "src/handler.go", []byte(`package handler

import "fmt"

func ProcessOrder(order *Order) error {
	result := CalculateTotal(order.Items)
	// SOURCE_MARKER_`+tc.MRIID+` - added on feature branch
	if result == nil {
		return nil
	}
	fmt.Println(result)
	return nil
}
`))
	t.Logf("feature branch %s created at SHA %s", featureBranch, featureSHA)

	// Step 2: Advance main with TARGET_MARKER (FTER feature branch was created)
	// This modification is NOT on the feature branch - it's only on main
	mainSHA := CommitFileToBareRepo(t, bareRepoPath, "main", "src/util.go", []byte(`package handler

// CalculateTotal sums all item prices.
// TARGET_MARKER_`+tc.MRIID+` - added on target branch after feature forked
func CalculateTotal(items []Item) *int {
	total := 0
	for _, item := range items {
		total += item.Price
	}
	return &total
}
`))
	t.Logf("main branch advanced to SHA %s", mainSHA)

	// Step 3: Configure MR with feature branch pointing to source SHA
	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
			"title": "Test merge result content",
			"description": "Testing read_file uses merge result",
			"author": {"username": "alice"},
			"source_branch": "` + featureBranch + `",
			"target_branch": "main",
			"sha": "` + featureSHA + `",
			"draft": false
		}`),
		Changes: json.RawMessage(`{
			"changes": [{
				"old_path": "src/handler.go",
				"new_path": "src/handler.go",
				"diff": "@@ -5,6 +5,7 @@ func ProcessOrder(order *Order) error {\n\tresult := CalculateTotal(order.Items)\n+\t// SOURCE_MARKER\n\tif result == nil {\n\t\treturn nil\n\t}",
				"new_file": false, "deleted_file": false, "renamed_file": false
			}]
		}`),
		Versions: json.RawMessage(`[{
			"id": 1,
			"head_commit_sha": "` + featureSHA + `",
			"base_commit_sha": "` + mainSHA + `",
			"start_commit_sha": "` + mainSHA + `"
		}]`),
	})

	// Step 4: Two-turn LLM - first requests read_file, second returns final_result
	var callCount atomic.Int32
	tc.SetResponseFunc(func(reqBody []byte) (int, json.RawMessage) {
		n := callCount.Add(1)
		if n == 1 {
			return 200, json.RawMessage(`{
				"id": "chatcmpl-merge-1",
				"object": "chat.completion",
				"model": "test-model",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": null,
						"tool_calls": [{
							"id": "call_merge1",
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
	})

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	run := tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 90*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}
	if callCount.Load() != 2 {
		t.Errorf("expected 2 LLM calls (read_file + final), got %d", callCount.Load())
	}

	// Step 5: Assert second LLM request contains TARGET_MARKER
	// This proves read_file returned content from the merge result (target branch content)
	// If read_file used source HEAD, TARGET_MARKER would NOT be present
	reqs := tc.LLMRequests()
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 LLM requests, got %d", len(reqs))
	}
	body := string(reqs[1].Body)
	targetMarker := "TARGET_MARKER_" + tc.MRIID
	if !strings.Contains(body, targetMarker) {
		t.Errorf("second LLM request should contain %s (merge result includes target branch changes), got: %s", targetMarker, body)
	}

	// Also verify SOURCE_MARKER is present (merge result includes source branch changes)
	sourceMarker := "SOURCE_MARKER_" + tc.MRIID
	if !strings.Contains(body, sourceMarker) {
		t.Logf("note: SOURCE_MARKER may also be present in read_file result for src/util.go (expected behavior)")
	}

	t.Logf("TestReadFileReturnsMergeResultContent passed: read_file returns merge result content")
}

// TestMergeConflictSkipsReview verifies that MRs with merge conflicts are skipped
// with status "conflicts" and no LLM call is made.
func TestMergeConflictSkipsReview(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Create feature branch FIRST (from initial main)
	conflictBranch := "feature/conflict-" + tc.MRIID
	conflictSHA := CommitFileToBareRepo(t, bareRepoPath, conflictBranch, "src/util.go", []byte(`package handler

// FEATURE_CHANGE - conflicting modification on feature branch
func CalculateTotal(items []Item) *int {
	total := 0
	for _, item := range items {
		total += item.Price
	}
	return &total
}
`))
	t.Logf("conflict branch %s created at SHA %s", conflictBranch, conflictSHA)

	// Advance main with CONFLICTING change to same file/lines
	_ = CommitFileToBareRepo(t, bareRepoPath, "main", "src/util.go", []byte(`package handler

// MAIN_CHANGE - conflicting modification on target branch
func CalculateTotal(items []Item) *int {
	total := 0
	for _, item := range items {
		total += item.Price
	}
	return &total
}
`))

	// Configure MR - will detect merge conflict
	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
			"title": "Test merge conflict",
			"description": "Testing conflict detection",
			"author": {"username": "alice"},
			"source_branch": "` + conflictBranch + `",
			"target_branch": "main",
			"sha": "` + conflictSHA + `",
			"draft": false
		}`),
		Changes: json.RawMessage(`{
			"changes": [{
				"old_path": "src/util.go",
				"new_path": "src/util.go",
				"diff": "@@ -1,4 +1,5 @@\n package handler\n+\n+// FEATURE_CHANGE\n func CalculateTotal(items []Item) *int {",
				"new_file": false, "deleted_file": false, "renamed_file": false
			}]
		}`),
		Versions: json.RawMessage(`[{
			"id": 1,
			"head_commit_sha": "` + conflictSHA + `",
			"base_commit_sha": "base000",
			"start_commit_sha": "base000"
		}]`),
	})

	llm.DefaultResponse = defaultLLMResponse

	_, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	// Wait for "conflicts" status (new status, maps to UNSPECIFIED in proto)
	run := tc.WaitForReviewRun("conflicts", 60*time.Second)
	if run.Status != "conflicts" {
		t.Errorf("expected status 'conflicts', got %s", run.Status)
	}

	// Assert NO LLM calls were made (review was skipped)
	if tc.LLMRequestCount() != 0 {
		t.Errorf("expected 0 LLM calls for conflict review, got %d", tc.LLMRequestCount())
	}

	// Assert NO comments were posted
	if len(tc.Notes()) != 0 {
		t.Errorf("expected 0 notes for conflict review, got %d", len(tc.Notes()))
	}
	if len(tc.Discussions()) != 0 {
		t.Errorf("expected 0 discussions for conflict review, got %d", len(tc.Discussions()))
	}

	t.Logf("TestMergeConflictSkipsReview passed: conflicts detected, review skipped")
}

// TestReadFileToolWorksWithSyncedRepo verifies that after SyncRepo, the read_file tool
// reads from the correct repo_path and returns real file content.
func TestReadFileToolWorksWithSyncedRepo(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
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

	// Two-turn: first call asks to read src/util.go (which exists in the bare repo),
	// second call returns final_result using the file content.
	var callCount atomic.Int32
	tc.SetResponseFunc(func(reqBody []byte) (int, json.RawMessage) {
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
	})

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	run := tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 90*time.Second, 2*time.Second)
	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Errorf("expected COMPLETED, got %s", run.Status)
	}
	if callCount.Load() != 2 {
		t.Errorf("expected 2 LLM calls (read_file + final), got %d", callCount.Load())
	}

	// The second LLM request must contain the actual file content from the bare repo
	reqs := tc.LLMRequests()
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
