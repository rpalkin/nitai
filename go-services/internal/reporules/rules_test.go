package reporules

import (
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestReadRepoRulesFileExists(t *testing.T) {
	repoPath, sha := createTestRepo(t, map[string]string{
		".review-rules.yaml": `instructions:
  - "Check for bugs"
  - "Review carefully"
ignore:
  - "vendor/**"
  - "**/*.pb.go"
`,
		"main.go": "package main\n",
	})
	defer os.RemoveAll(repoPath)

	rules, err := ReadRepoRules(repoPath, sha, []string{"main.go"})
	if err != nil {
		t.Fatalf("ReadRepoRules: %v", err)
	}
	if len(rules.Instructions) != 2 {
		t.Errorf("expected 2 instructions, got %d", len(rules.Instructions))
	}
	if rules.Instructions[0] != "Check for bugs" {
		t.Errorf("expected 'Check for bugs', got %q", rules.Instructions[0])
	}
	if len(rules.IgnoreGlobs) != 2 {
		t.Errorf("expected 2 ignore globs, got %d", len(rules.IgnoreGlobs))
	}
	if rules.RulesModified {
		t.Errorf("expected RulesModified=false when .review-rules.yaml not in changed files")
	}
}

func TestReadRepoRulesFileNotExists(t *testing.T) {
	repoPath, sha := createTestRepo(t, map[string]string{
		"main.go": "package main\n",
	})
	defer os.RemoveAll(repoPath)

	rules, err := ReadRepoRules(repoPath, sha, []string{"main.go"})
	if err != nil {
		t.Fatalf("ReadRepoRules: %v", err)
	}
	if len(rules.Instructions) != 0 || len(rules.IgnoreGlobs) != 0 {
		t.Errorf("expected empty rules when file not found, got %+v", rules)
	}
}

func TestReadRepoRulesInvalidYAML(t *testing.T) {
	repoPath, sha := createTestRepo(t, map[string]string{
		".review-rules.yaml": `invalid yaml content: [unclosed
`,
		"main.go": "package main\n",
	})
	defer os.RemoveAll(repoPath)

	_, err := ReadRepoRules(repoPath, sha, []string{"main.go"})
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestReadRepoRulesSHANotFound(t *testing.T) {
	repoPath, _ := createTestRepo(t, map[string]string{
		"main.go": "package main\n",
	})
	defer os.RemoveAll(repoPath)

	rules, err := ReadRepoRules(repoPath, "nonexistent-sha-1234567890abcdef", []string{"main.go"})
	if err != nil {
		t.Fatalf("expected no error for SHA not found, got %v", err)
	}
	if len(rules.Instructions) != 0 || len(rules.IgnoreGlobs) != 0 {
		t.Errorf("expected empty rules when SHA not found, got %+v", rules)
	}
}

func TestReadRepoRulesModified(t *testing.T) {
	repoPath, sha := createTestRepo(t, map[string]string{
		".review-rules.yaml": `instructions:
  - "Check for bugs"
`,
		"main.go": "package main\n",
	})
	defer os.RemoveAll(repoPath)

	rules, err := ReadRepoRules(repoPath, sha, []string{".review-rules.yaml", "main.go"})
	if err != nil {
		t.Fatalf("ReadRepoRules: %v", err)
	}
	if !rules.RulesModified {
		t.Errorf("expected RulesModified=true when .review-rules.yaml in changed files")
	}
}

func TestReadRepoRulesEmptyFile(t *testing.T) {
	repoPath, sha := createTestRepo(t, map[string]string{
		".review-rules.yaml": "",
		"main.go":            "package main\n",
	})
	defer os.RemoveAll(repoPath)

	rules, err := ReadRepoRules(repoPath, sha, []string{"main.go"})
	if err != nil {
		t.Fatalf("ReadRepoRules: %v", err)
	}
	if len(rules.Instructions) != 0 || len(rules.IgnoreGlobs) != 0 {
		t.Errorf("expected empty rules for empty file, got %+v", rules)
	}
}

func createTestRepo(t *testing.T, files map[string]string) (repoPath string, sha string) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "reporules-test-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}

	repo, err := gogit.PlainInit(tmpDir, false)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("git init: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("worktree: %v", err)
	}

	for relPath, content := range files {
		absPath := filepath.Join(tmpDir, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			os.RemoveAll(tmpDir)
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			os.RemoveAll(tmpDir)
			t.Fatalf("write file: %v", err)
		}
		if _, err := wt.Add(relPath); err != nil {
			os.RemoveAll(tmpDir)
			t.Fatalf("git add: %v", err)
		}
	}

	commit, err := wt.Commit("test commit", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com"},
	})
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("git commit: %v", err)
	}

	return tmpDir, commit.String()
}
