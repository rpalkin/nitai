//go:build e2e

package e2e

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	apiv1 "ai-reviewer/gen/api/v1"
)

// TestReviewRulesIgnoreFilters verifies that .review-rules.yaml ignores files matching globs.
// Files matching ignore patterns are removed from the diff and changed_files before LLM sees them.
func TestReviewRulesIgnoreFilters(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Commit .review-rules.yaml with ignore patterns to a unique branch
	rulesSHA := CommitFileToBareRepo(t, bareRepoPath, "rules-ignore-"+tc.MRIID, ".review-rules.yaml", []byte(`ignore:
  - "vendor/**"
  - "**/*.pb.go"
`))
	t.Logf("committed .review-rules.yaml at SHA %s", rulesSHA)

	// Configure MR with changed files including ignored ones
	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
			"title": "Add vendor and generated files",
			"description": "Testing ignore rules",
			"author": {"username": "alice"},
			"source_branch": "rules-ignore-` + tc.MRIID + `",
			"target_branch": "main",
			"sha": "` + rulesSHA + `",
			"draft": false
		}`),
		Changes: json.RawMessage(`{
			"changes": [
				{"old_path": "main.go", "new_path": "main.go",
				 "diff": "@@ -1,2 +1,3 @@\n package main\n+// new line\n func main() {}",
				 "new_file": false, "deleted_file": false, "renamed_file": false},
				{"old_path": "vendor/dep.go", "new_path": "vendor/dep.go",
				 "diff": "@@ -1,2 +1,3 @@\n package vendor\n+// vendor change\n func Dep() {}",
				 "new_file": false, "deleted_file": false, "renamed_file": false},
				{"old_path": "generated.pb.go", "new_path": "generated.pb.go",
				 "diff": "@@ -1,2 +1,3 @@\n package gen\n+// generated\n func Generated() {}",
				 "new_file": false, "deleted_file": false, "renamed_file": false}
			]
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

	run := tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 90*time.Second, 2*time.Second)

	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Fatalf("expected COMPLETED, got %s", run.Status)
	}

	// Assert: LLM request should NOT contain vendor/dep.go or generated.pb.go diffs
	llmReqs := tc.LLMRequests()
	if len(llmReqs) == 0 {
		t.Fatalf("expected at least 1 LLM request")
	}
	body := string(llmReqs[0].Body)
	if strings.Contains(body, "vendor/dep.go") {
		t.Errorf("LLM request should NOT contain ignored file vendor/dep.go")
	}
	if strings.Contains(body, "generated.pb.go") {
		t.Errorf("LLM request should NOT contain ignored file generated.pb.go")
	}
	if !strings.Contains(body, "main.go") {
		t.Errorf("LLM request should contain non-ignored file main.go")
	}

	// Assert: changed_files should only contain main.go
	if !strings.Contains(body, `"changed_files"`) {
		t.Logf("warning: LLM request missing changed_files field")
	} else {
		if strings.Contains(body, `"vendor/dep.go"`) {
			t.Errorf("changed_files should NOT contain vendor/dep.go")
		}
		if !strings.Contains(body, `"main.go"`) {
			t.Errorf("changed_files should contain main.go")
		}
	}

	t.Logf("TestReviewRulesIgnoreFilters passed: ignored files excluded from LLM request")
}

// TestReviewRulesInstructions verifies that instructions from .review-rules.yaml are passed to the LLM.
func TestReviewRulesInstructions(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Commit .review-rules.yaml with instructions
	rulesSHA := CommitFileToBareRepo(t, bareRepoPath, "rules-instr-"+tc.MRIID, ".review-rules.yaml", []byte(`instructions:
  - "Always check error handling"
  - "Look for security issues"
`))
	t.Logf("committed .review-rules.yaml at SHA %s", rulesSHA)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
			"title": "Add feature",
			"description": "Testing instructions",
			"author": {"username": "alice"},
			"source_branch": "rules-instr-` + tc.MRIID + `",
			"target_branch": "main",
			"sha": "` + rulesSHA + `",
			"draft": false
		}`),
		Changes: json.RawMessage(`{
			"changes": [{
				"old_path": "handler.go", "new_path": "handler.go",
				"diff": "@@ -1,2 +1,3 @@\n package main\n+// new line\n func Handler() {}",
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

	// Assert: LLM request should contain the instructions
	llmReqs := tc.LLMRequests()
	if len(llmReqs) == 0 {
		t.Fatalf("expected at least 1 LLM request")
	}
	body := string(llmReqs[0].Body)
	if !strings.Contains(body, "Always check error handling") {
		t.Errorf("LLM request should contain instruction 'Always check error handling'")
	}
	if !strings.Contains(body, "Look for security issues") {
		t.Errorf("LLM request should contain instruction 'Look for security issues'")
	}

	t.Logf("TestReviewRulesInstructions passed: instructions found in LLM request")
}

// TestReviewRulesModifiedWarning verifies that when .review-rules.yaml is modified in the PR,
// a warning is posted in the summary note.
func TestReviewRulesModifiedWarning(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Commit .review-rules.yaml
	rulesSHA := CommitFileToBareRepo(t, bareRepoPath, "rules-mod-"+tc.MRIID, ".review-rules.yaml", []byte(`instructions:
  - "Check for bugs"
`))
	t.Logf("committed .review-rules.yaml at SHA %s", rulesSHA)

	// MR includes .review-rules.yaml in changed_files
	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
			"title": "Update review rules",
			"description": "Modified config",
			"author": {"username": "alice"},
			"source_branch": "rules-mod-` + tc.MRIID + `",
			"target_branch": "main",
			"sha": "` + rulesSHA + `",
			"draft": false
		}`),
		Changes: json.RawMessage(`{
			"changes": [
				{"old_path": ".review-rules.yaml", "new_path": ".review-rules.yaml",
				 "diff": "@@ -1,2 +1,3 @@\n instructions:\n+  - 'Check for bugs'\n   - 'Old rule'\n",
				 "new_file": false, "deleted_file": false, "renamed_file": false},
				{"old_path": "main.go", "new_path": "main.go",
				 "diff": "@@ -1,2 +1,3 @@\n package main\n+// change\n func main() {}",
				 "new_file": false, "deleted_file": false, "renamed_file": false}
			]
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

	// Assert: posted summary note contains the warning
	notes := tc.Notes()
	if len(notes) == 0 {
		t.Fatalf("expected at least 1 posted note")
	}
	if !strings.Contains(notes[0].Body, "modifies `.review-rules.yaml`") {
		t.Errorf("summary note should contain '.review-rules.yaml' warning, got: %s", notes[0].Body)
	}

	t.Logf("TestReviewRulesModifiedWarning passed: warning found in summary")
}

// TestReviewRulesMissingFile verifies that missing .review-rules.yaml is a silent no-op.
func TestReviewRulesMissingFile(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// No .review-rules.yaml committed - use a branch without the rules file
	// Create a commit without rules file on the branch
	noRulesSHA := CommitFileToBareRepo(t, bareRepoPath, "no-rules-"+tc.MRIID, "dummy.txt", []byte("// no rules file in this branch\n"))

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
			"title": "Feature without rules",
			"description": "No config",
			"author": {"username": "alice"},
			"source_branch": "no-rules-` + tc.MRIID + `",
			"target_branch": "main",
			"sha": "` + noRulesSHA + `",
			"draft": false
		}`),
		Changes: json.RawMessage(`{
			"changes": [{
				"old_path": "feature.go", "new_path": "feature.go",
				"diff": "@@ -1,2 +1,3 @@\n package main\n+// feature\n func Feature() {}",
				"new_file": false, "deleted_file": false, "renamed_file": false
			}]
		}`),
		Versions: json.RawMessage(`[{
			"id": 1,
			"head_commit_sha": "` + noRulesSHA + `",
			"base_commit_sha": "base000",
			"start_commit_sha": "base000"
		}]`),
	})

	llm.DefaultResponse = defaultLLMResponse

	runID, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	run := tc.PollReviewRun(runID,
		apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED, 90*time.Second, 2*time.Second)

	if run.Status != apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED {
		t.Fatalf("expected COMPLETED (no error), got %s", run.Status)
	}

	// No filtering should have occurred - all files present
	llmReqs := tc.LLMRequests()
	if len(llmReqs) == 0 {
		t.Fatalf("expected at least 1 LLM request")
	}
	body := string(llmReqs[0].Body)
	if !strings.Contains(body, "feature.go") {
		t.Errorf("LLM request should contain the change file")
	}

	t.Logf("TestReviewRulesMissingFile passed: review completed normally without rules")
}

// TestReviewRulesAllFilesIgnored verifies that if all files are ignored, the review is skipped.
func TestReviewRulesAllFilesIgnored(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	// Commit .review-rules.yaml that ignores all .go files
	rulesSHA := CommitFileToBareRepo(t, bareRepoPath, "rules-all-ignore-"+tc.MRIID, ".review-rules.yaml", []byte(`ignore:
  - "**/*.go"
`))
	t.Logf("committed .review-rules.yaml at SHA %s", rulesSHA)

	tc.SetMR(&MRConfig{
		Details: json.RawMessage(`{
			"title": "All ignored",
			"description": "Everything skipped",
			"author": {"username": "alice"},
			"source_branch": "rules-all-ignore-` + tc.MRIID + `",
			"target_branch": "main",
			"sha": "` + rulesSHA + `",
			"draft": false
		}`),
		Changes: json.RawMessage(`{
			"changes": [
				{"old_path": "main.go", "new_path": "main.go",
				 "diff": "@@ -1,2 +1,3 @@\n package main\n+// change\n func main() {}",
				 "new_file": false, "deleted_file": false, "renamed_file": false},
				{"old_path": "util.go", "new_path": "util.go",
				 "diff": "@@ -1,2 +1,3 @@\n package main\n+// util change\n func Util() {}",
				 "new_file": false, "deleted_file": false, "renamed_file": false}
			]
		}`),
		Versions: json.RawMessage(`[{
			"id": 1,
			"head_commit_sha": "` + rulesSHA + `",
			"base_commit_sha": "base000",
			"start_commit_sha": "base000"
		}]`),
	})

	llm.DefaultResponse = defaultLLMResponse

	// Trigger review - should complete but be skipped
	_, err := tc.TriggerReview()
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}

	// Wait for skipped status
	dbRun := tc.WaitForReviewRun("skipped", 90*time.Second)
	t.Logf("review run status: %s", dbRun.Status)

	if dbRun.Status != "skipped" {
		t.Errorf("expected skipped status when all files ignored, got %s", dbRun.Status)
	}

	// No LLM call should have been made for this test's marker
	if tc.LLMRequestCount() != 0 {
		t.Errorf("expected 0 LLM calls when all files ignored, got %d", tc.LLMRequestCount())
	}

	t.Logf("TestReviewRulesAllFilesIgnored passed: review skipped when all files ignored")
}
