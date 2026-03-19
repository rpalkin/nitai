//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var (
	stack   *E2EStack
	clients *TestClients
	gitlab  *GitLabMock
	llm     *LLMMock
)

func TestMain(m *testing.M) {
	// 1. Start mock servers FIRST (before Docker containers need them)
	gitlab = NewGitLabMock()
	llm = NewLLMMock()

	// 2. Configure mock GitLab with default project list
	gitlab.SetProjects([]GitLabProject{
		{ID: 100, Name: "test-project", PathWithNamespace: "group/test-project", HTTPURLToRepo: "http://gitlab.example.com/group/test-project.git", DefaultBranch: "main"},
	})

	// 3. Create a bare git repo served via dumb HTTP for RepoSyncer tests.
	//    The repo has test files with known content used by tool integration tests.
	t0 := &testMainT{}
	gitRepoDir := setupBareGitRepo(t0)
	defer os.RemoveAll(gitRepoDir)
	gitlab.SetGitRepoPath(gitRepoDir)

	// 4. Start Docker Compose stack
	stack = StartStack(t0, gitlab, llm)
	clients = stack.Clients

	// 5. Run tests
	code := m.Run()

	// 6. Teardown (skip if tests failed to allow debugging)
	if code != 0 {
		t0.Logf("Tests failed with exit code %d. Keeping stack running for debugging.", code)
		t0.Logf("To clean up manually: docker compose -p e2e down -v")
	} else {
		StopStack(t0, stack)
		gitlab.Server.Close()
		llm.Server.Close()
	}

	os.Exit(code)
}

// setupBareGitRepo creates a temporary bare git repository with test files.
// Returns the path to the bare repo directory (caller must os.RemoveAll it).
func setupBareGitRepo(t testingT) string {
	// Create a working dir to build the repo, then clone as bare
	workDir, err := os.MkdirTemp("", "e2e-git-work-*")
	if err != nil {
		t.Fatalf("MkdirTemp (work): %v", err)
	}
	defer os.RemoveAll(workDir)

	bareDir, err := os.MkdirTemp("", "e2e-git-bare-*")
	if err != nil {
		t.Fatalf("MkdirTemp (bare): %v", err)
	}

	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Init working repo
	run(workDir, "init", "-b", "main")
	run(workDir, "config", "user.email", "test@example.com")
	run(workDir, "config", "user.name", "Test")

	// Create test files
	files := map[string]string{
		"src/handler.go": `package handler

import "fmt"

func ProcessOrder(order *Order) error {
	result := CalculateTotal(order.Items)
	if result == nil {
		return nil
	}
	fmt.Println(result)
	return nil
}
`,
		"src/util.go": `package handler

// CalculateTotal sums all item prices.
// func CalculateTotal(items []Item) *int
func CalculateTotal(items []Item) *int {
	total := 0
	for _, item := range items {
		total += item.Price
	}
	return &total
}
`,
		"pkg/mathutil/mathutil.go": `package mathutil

// Foo adds two integers.
func Foo(x, y int) int {
	return x + y
}
`,
	}

	for relPath, content := range files {
		absPath := filepath.Join(workDir, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			t.Fatalf("MkdirAll %s: %v", filepath.Dir(absPath), err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile %s: %v", relPath, err)
		}
	}

	run(workDir, "add", ".")
	run(workDir, "commit", "-m", "initial commit")

	// Clone as bare into bareDir (overwrites the temp dir contents)
	os.RemoveAll(bareDir)
	if err := exec.Command("git", "clone", "--bare", workDir, bareDir).Run(); err != nil {
		t.Fatalf("git clone --bare: %v", err)
	}

	// Enable dumb HTTP protocol
	if err := exec.Command("git", "--git-dir="+bareDir, "update-server-info").Run(); err != nil {
		t.Fatalf("git update-server-info: %v", err)
	}

	t.Logf("bare git repo created at %s", bareDir)
	return bareDir
}
