package localmerge

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateMergeCommit_CleanMerge(t *testing.T) {
	repoPath := createBareRepo(t)
	defer os.RemoveAll(repoPath)

	branchA := "branch-a-" + strings.ReplaceAll(t.Name(), "/", "-")
	branchB := "branch-b-" + strings.ReplaceAll(t.Name(), "/", "-")

	shaA := commitFileToBareRepo(t, repoPath, branchA, "file_a.txt", []byte("content A\n"))
	shaB := commitFileToBareRepo(t, repoPath, branchB, "file_b.txt", []byte("content B\n"))

	_ = getMainHead(t, repoPath)

	result, err := CreateMergeCommit(repoPath, shaA, shaB)
	if err != nil {
		t.Fatalf("CreateMergeCommit: %v", err)
	}
	if result.HasConflicts {
		t.Error("expected HasConflicts=false for clean merge")
	}
	if result.MergeSHA == "" {
		t.Error("expected non-empty MergeSHA")
	}

	mergeContent := getFileAtCommit(t, repoPath, result.MergeSHA, "file_a.txt")
	if !strings.Contains(mergeContent, "content A") {
		t.Errorf("merge result missing file_a.txt content, got: %s", mergeContent)
	}
	mergeContent = getFileAtCommit(t, repoPath, result.MergeSHA, "file_b.txt")
	if !strings.Contains(mergeContent, "content B") {
		t.Errorf("merge result missing file_b.txt content, got: %s", mergeContent)
	}

	t.Logf("clean merge: branchA=%s, branchB=%s, merge=%s", shaA[:8], shaB[:8], result.MergeSHA[:8])
}

func TestCreateMergeCommit_Conflict(t *testing.T) {
	repoPath := createBareRepo(t)
	defer os.RemoveAll(repoPath)

	featureBranch := "feature-conflict-" + strings.ReplaceAll(t.Name(), "/", "-")

	mainSHA := commitFileToBareRepo(t, repoPath, "main", "conflict.txt", []byte("MAIN_CONTENT\n"))
	featureSHA := commitFileToBareRepoFromSHA(t, repoPath, featureBranch, initialCommitSHA(t, repoPath), "conflict.txt", []byte("FEATURE_CONTENT\n"))

	result, err := CreateMergeCommit(repoPath, mainSHA, featureSHA)
	if err != nil {
		t.Fatalf("CreateMergeCommit: %v", err)
	}
	if !result.HasConflicts {
		t.Error("expected HasConflicts=true for conflicting merge")
	}
	if result.MergeSHA != "" {
		t.Error("expected empty MergeSHA for conflicting merge")
	}

	t.Logf("conflict detected: main=%s, feature=%s", mainSHA[:8], featureSHA[:8])
}

func TestCreateMergeCommit_FastForward(t *testing.T) {
	repoPath := createBareRepo(t)
	defer os.RemoveAll(repoPath)

	featureBranch := "feature-ff-" + strings.ReplaceAll(t.Name(), "/", "-")

	featureSHA := commitFileToBareRepo(t, repoPath, featureBranch, "new_file.txt", []byte("feature content\n"))
	mainSHA := getMainHead(t, repoPath)

	result, err := CreateMergeCommit(repoPath, mainSHA, featureSHA)
	if err != nil {
		t.Fatalf("CreateMergeCommit: %v", err)
	}
	if result.HasConflicts {
		t.Error("expected HasConflicts=false for fast-forward merge")
	}
	if result.MergeSHA == "" {
		t.Error("expected non-empty MergeSHA")
	}

	mergeContent := getFileAtCommit(t, repoPath, result.MergeSHA, "new_file.txt")
	if !strings.Contains(mergeContent, "feature content") {
		t.Errorf("merge result missing new_file.txt content, got: %s", mergeContent)
	}

	t.Logf("fast-forward merge: main=%s, feature=%s, merge=%s", mainSHA[:8], featureSHA[:8], result.MergeSHA[:8])
}

func TestCreateMergeCommit_InvalidSHA(t *testing.T) {
	repoPath := createBareRepo(t)
	defer os.RemoveAll(repoPath)

	mainSHA := getMainHead(t, repoPath)

	_, err := CreateMergeCommit(repoPath, mainSHA, "invalid-sha-12345678")
	if err == nil {
		t.Error("expected error for invalid SHA")
	}
}

func createBareRepo(t *testing.T) string {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "localmerge-test-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(tmpDir, "initial.txt"), []byte("initial\n"), 0644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "initial commit")

	bareDir, err := os.MkdirTemp("", "localmerge-bare-*")
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("bare dir: %v", err)
	}
	os.RemoveAll(bareDir)

	cloneCmd := exec.Command("git", "clone", "--bare", tmpDir, bareDir)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}

	os.RemoveAll(tmpDir)
	return bareDir
}

func getMainHead(t *testing.T, repoPath string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir="+repoPath, "rev-parse", "main")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse main: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func initialCommitSHA(t *testing.T, repoPath string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir="+repoPath, "rev-list", "--max-parents=0", "main")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-list --max-parents=0: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func commitFileToBareRepo(t *testing.T, repoPath, branch, filePath string, content []byte) string {
	t.Helper()

	workDir, err := os.MkdirTemp("", "localmerge-work-*")
	if err != nil {
		t.Fatalf("work dir: %v", err)
	}
	defer os.RemoveAll(workDir)

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	cloneCmd := exec.Command("git", "clone", "--branch", branch, repoPath, workDir)
	cloneCmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if _, err := cloneCmd.CombinedOutput(); err != nil {
		cloneCmd = exec.Command("git", "clone", repoPath, workDir)
		cloneCmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cloneCmd.CombinedOutput(); err != nil {
			t.Fatalf("git clone: %v\n%s", err, out)
		}
		runGit("checkout", "-b", branch)
	}

	absPath := filepath.Join(workDir, filePath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(absPath, content, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	runGit("add", filePath)
	runGit("commit", "-m", "add "+filePath)
	runGit("push", "origin", branch)

	revParseCmd := exec.Command("git", "rev-parse", "HEAD")
	revParseCmd.Dir = workDir
	shaBytes, err := revParseCmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}

	cmd := exec.Command("git", "--git-dir="+repoPath, "update-server-info")
	cmd.Run()

	return strings.TrimSpace(string(shaBytes))
}

func commitFileToBareRepoFromSHA(t *testing.T, repoPath, branch, baseSHA, filePath string, content []byte) string {
	t.Helper()

	workDir, err := os.MkdirTemp("", "localmerge-work-*")
	if err != nil {
		t.Fatalf("work dir: %v", err)
	}
	defer os.RemoveAll(workDir)

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	cloneCmd := exec.Command("git", "clone", repoPath, workDir)
	cloneCmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}

	runGit("checkout", "-b", branch, baseSHA)

	absPath := filepath.Join(workDir, filePath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(absPath, content, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	runGit("add", filePath)
	runGit("commit", "-m", "add "+filePath+" from base "+baseSHA[:8])
	runGit("push", "origin", branch)

	revParseCmd := exec.Command("git", "rev-parse", "HEAD")
	revParseCmd.Dir = workDir
	shaBytes, err := revParseCmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}

	cmd := exec.Command("git", "--git-dir="+repoPath, "update-server-info")
	cmd.Run()

	return strings.TrimSpace(string(shaBytes))
}

func getFileAtCommit(t *testing.T, repoPath, sha, filePath string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir="+repoPath, "show", sha+":"+filePath)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("git show %s:%s: %v\n%s", sha, filePath, err, string(exitErr.Stderr))
		}
		t.Fatalf("git show %s:%s: %v", sha, filePath, err)
	}
	return string(out)
}
