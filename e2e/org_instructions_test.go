//go:build e2e

package e2e

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	apiv1 "ai-reviewer/gen/api/v1"
)

// TestOrgInstructionsInReviewPrompt verifies that org-level instructions
// are merged into the LLM prompt correctly.
func TestOrgInstructionsInReviewPrompt(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Create 3 instructions:
	// A: no filters — should match
	instA, err := clients.Instruction.CreateInstruction(tc.T.Context(),
		connect.NewRequest(&apiv1.CreateInstructionRequest{
			Name:    "Instruction A",
			Content: "Always check for nil pointer dereferences",
		}))
	if err != nil {
		t.Fatalf("CreateInstruction A: %v", err)
	}
	instAID := instA.Msg.Instruction.Id

	// B: matches this repo + *.go files — should match
	instB, err := clients.Instruction.CreateInstruction(tc.T.Context(),
		connect.NewRequest(&apiv1.CreateInstructionRequest{
			Name:              "Instruction B",
			Content:           "Verify Go error wrapping uses fmt.Errorf with %w",
			RepoFilter:        []string{tc.RepoID},
			FilePatternFilter: []string{"*.go"},
		}))
	if err != nil {
		t.Fatalf("CreateInstruction B: %v", err)
	}
	instBID := instB.Msg.Instruction.Id

	// C: different repo — should NOT match
	otherRepoID := uuid.New().String()
	instC, err := clients.Instruction.CreateInstruction(tc.T.Context(),
		connect.NewRequest(&apiv1.CreateInstructionRequest{
			Name:       "Instruction C",
			Content:    "This should not appear in LLM prompt",
			RepoFilter: []string{otherRepoID},
		}))
	if err != nil {
		t.Fatalf("CreateInstruction C: %v", err)
	}
	instCID := instC.Msg.Instruction.Id

	// Clean up all created instructions after test
	t.Cleanup(func() {
		clients.Instruction.DeleteInstruction(tc.T.Context(), connect.NewRequest(&apiv1.DeleteInstructionRequest{Id: instAID}))
		clients.Instruction.DeleteInstruction(tc.T.Context(), connect.NewRequest(&apiv1.DeleteInstructionRequest{Id: instBID}))
		clients.Instruction.DeleteInstruction(tc.T.Context(), connect.NewRequest(&apiv1.DeleteInstructionRequest{Id: instCID}))
	})

	// Set up MR with a .go file change (no .review-rules.yaml)
	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
			"title": "Add Go feature",
			"description": "Testing org instructions",
			"author": {"username": "alice"},
			"source_branch": "feature-` + tc.MRIID + `",
			"target_branch": "main",
			"sha": "abc123",
			"draft": false
		}`),
		Changes: json.RawMessage(`{
			"changes": [{
				"old_path": "main.go", "new_path": "main.go",
				"diff": "@@ -1,2 +1,3 @@\n package main\n+// feature\n func main() {}",
				"new_file": false, "deleted_file": false, "renamed_file": false
			}]
		}`),
		Versions: json.RawMessage(`[{
			"id": 1,
			"head_commit_sha": "abc123",
			"base_commit_sha": "base000",
			"start_commit_sha": "base000"
		}]`),
	})

	llm.DefaultResponse = defaultLLMResponse

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 90*time.Second, 2*time.Second)

	// Assert: LLM request contains Instruction A and B, but NOT C
	llmReqs := tc.LLMRequests()
	if len(llmReqs) == 0 {
		t.Fatalf("expected at least 1 LLM request")
	}
	body := string(llmReqs[0].Body)

	// Instruction A (no filters) should appear
	if !strings.Contains(body, "Always check for nil pointer dereferences") {
		t.Errorf("LLM request should contain Instruction A content")
	}

	// Instruction B (repo+file filter match) should appear
	if !strings.Contains(body, "Verify Go error wrapping uses fmt.Errorf with %w") {
		t.Errorf("LLM request should contain Instruction B content")
	}

	// Instruction C (different repo) should NOT appear
	if strings.Contains(body, "This should not appear in LLM prompt") {
		t.Errorf("LLM request should NOT contain Instruction C content")
	}

	t.Logf("TestOrgInstructionsInReviewPrompt passed: instructions filtered correctly")
}

// TestOrgInstructionsMergedWithYamlRules verifies that org instructions
// and .review-rules.yaml instructions are both passed to the LLM.
func TestOrgInstructionsMergedWithYamlRules(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Create 1 API instruction
	inst, err := clients.Instruction.CreateInstruction(tc.T.Context(),
		connect.NewRequest(&apiv1.CreateInstructionRequest{
			Name:    "API Instruction",
			Content: "Check for SQL injection vulnerabilities",
		}))
	if err != nil {
		t.Fatalf("CreateInstruction: %v", err)
	}
	instID := inst.Msg.Instruction.Id

	// Clean up after test
	t.Cleanup(func() {
		clients.Instruction.DeleteInstruction(tc.T.Context(), connect.NewRequest(&apiv1.DeleteInstructionRequest{Id: instID}))
	})

	// Commit .review-rules.yaml with additional instructions
	rulesSHA := CommitFileToBareRepo(t, bareRepoPath, "rules-merge-"+tc.MRIID, ".review-rules.yaml", []byte(`instructions:
  - "Always check error handling"
`))
	t.Logf("committed .review-rules.yaml at SHA %s", rulesSHA)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
			"title": "Feature with rules",
			"description": "Testing merged instructions",
			"author": {"username": "alice"},
			"source_branch": "rules-merge-` + tc.MRIID + `",
			"target_branch": "main",
			"sha": "` + rulesSHA + `",
			"draft": false
		}`),
		Changes: json.RawMessage(`{
			"changes": [{
				"old_path": "handler.go", "new_path": "handler.go",
				"diff": "@@ -1,2 +1,3 @@\n package main\n+// change\n func Handler() {}",
				"new_file": false, "deleted_file": false, "renamed_file": false
			}]
		}`),
		Versions: json.RawMessage(`[{
			"id": 1,
			"head_commit_sha": "` + rulesSHA + `",
			"base_commit_sha": "base000",
			"start_commit_sha": "base000"
		}]`),
	})

	llm.DefaultResponse = defaultLLMResponse

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 90*time.Second, 2*time.Second)

	// Assert: LLM request contains BOTH instructions
	llmReqs := tc.LLMRequests()
	if len(llmReqs) == 0 {
		t.Fatalf("expected at least 1 LLM request")
	}
	body := string(llmReqs[0].Body)

	// API instruction
	if !strings.Contains(body, "Check for SQL injection vulnerabilities") {
		t.Errorf("LLM request should contain API instruction")
	}

	// YAML instruction
	if !strings.Contains(body, "Always check error handling") {
		t.Errorf("LLM request should contain YAML instruction")
	}

	t.Logf("TestOrgInstructionsMergedWithYamlRules passed: both instruction sources merged")
}

// TestOrgInstructionsFilePatternFilter verifies file pattern matching works.
func TestOrgInstructionsFilePatternFilter(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Create instruction matching *.go files only
	inst, err := clients.Instruction.CreateInstruction(tc.T.Context(),
		connect.NewRequest(&apiv1.CreateInstructionRequest{
			Name:              "Go-only instruction",
			Content:           "Review Go code for idiomatic patterns",
			FilePatternFilter: []string{"*.go"},
		}))
	if err != nil {
		t.Fatalf("CreateInstruction: %v", err)
	}
	instID := inst.Msg.Instruction.Id

	// Clean up after test
	t.Cleanup(func() {
		clients.Instruction.DeleteInstruction(tc.T.Context(), connect.NewRequest(&apiv1.DeleteInstructionRequest{Id: instID}))
	})

	// MR with only .md file changes
	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
			"title": "Docs update",
			"description": "Only markdown files",
			"author": {"username": "alice"},
			"source_branch": "docs-` + tc.MRIID + `",
			"target_branch": "main",
			"sha": "doc123",
			"draft": false
		}`),
		Changes: json.RawMessage(`{
			"changes": [{
				"old_path": "README.md", "new_path": "README.md",
				"diff": "@@ -1,2 +1,3 @@\n # Project\n+## New section\n More docs",
				"new_file": false, "deleted_file": false, "renamed_file": false
			}]
		}`),
		Versions: json.RawMessage(`[{
			"id": 1,
			"head_commit_sha": "doc123",
			"base_commit_sha": "base000",
			"start_commit_sha": "base000"
		}]`),
	})

	llm.DefaultResponse = defaultLLMResponse

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 90*time.Second, 2*time.Second)

	// Assert: instruction should NOT appear (no matching files)
	llmReqs := tc.LLMRequests()
	if len(llmReqs) == 0 {
		t.Fatalf("expected at least 1 LLM request")
	}
	body := string(llmReqs[0].Body)

	if strings.Contains(body, "Review Go code for idiomatic patterns") {
		t.Errorf("LLM request should NOT contain instruction (file pattern mismatch)")
	}

	t.Logf("TestOrgInstructionsFilePatternFilter passed: file pattern filter working")
}

// TestOrgInstructionsRepoFilter verifies repo filter matching.
func TestOrgInstructionsRepoFilter(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Create instruction specific to THIS repo
	instA, err := clients.Instruction.CreateInstruction(tc.T.Context(),
		connect.NewRequest(&apiv1.CreateInstructionRequest{
			Name:       "Repo-specific instruction",
			Content:    "Check for project-specific patterns",
			RepoFilter: []string{tc.RepoID},
		}))
	if err != nil {
		t.Fatalf("CreateInstruction: %v", err)
	}
	instAID := instA.Msg.Instruction.Id

	// Create instruction for a different repo (should NOT match)
	otherRepoID := uuid.New().String()
	instB, err := clients.Instruction.CreateInstruction(tc.T.Context(),
		connect.NewRequest(&apiv1.CreateInstructionRequest{
			Name:       "Other-repo instruction",
			Content:    "This instruction should be filtered out",
			RepoFilter: []string{otherRepoID},
		}))
	if err != nil {
		t.Fatalf("CreateInstruction (other repo): %v", err)
	}
	instBID := instB.Msg.Instruction.Id

	// Clean up after test
	t.Cleanup(func() {
		clients.Instruction.DeleteInstruction(tc.T.Context(), connect.NewRequest(&apiv1.DeleteInstructionRequest{Id: instAID}))
		clients.Instruction.DeleteInstruction(tc.T.Context(), connect.NewRequest(&apiv1.DeleteInstructionRequest{Id: instBID}))
	})

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
			"title": "Feature",
			"description": "Testing repo filter",
			"author": {"username": "alice"},
			"source_branch": "feature-` + tc.MRIID + `",
			"target_branch": "main",
			"sha": "feat123",
			"draft": false
		}`),
		Changes: json.RawMessage(`{
			"changes": [{
				"old_path": "main.go", "new_path": "main.go",
				"diff": "@@ -1,2 +1,3 @@\n package main\n+// change\n func main() {}",
				"new_file": false, "deleted_file": false, "renamed_file": false
			}]
		}`),
		Versions: json.RawMessage(`[{
			"id": 1,
			"head_commit_sha": "feat123",
			"base_commit_sha": "base000",
			"start_commit_sha": "base000"
		}]`),
	})

	llm.DefaultResponse = defaultLLMResponse

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 90*time.Second, 2*time.Second)

	llmReqs := tc.LLMRequests()
	if len(llmReqs) == 0 {
		t.Fatalf("expected at least 1 LLM request")
	}
	body := string(llmReqs[0].Body)

	// Matching repo instruction should appear
	if !strings.Contains(body, "Check for project-specific patterns") {
		t.Errorf("LLM request should contain repo-matching instruction")
	}

	// Non-matching repo instruction should NOT appear
	if strings.Contains(body, "This instruction should be filtered out") {
		t.Errorf("LLM request should NOT contain non-matching repo instruction")
	}

	t.Logf("TestOrgInstructionsRepoFilter passed: repo filter working")
}
