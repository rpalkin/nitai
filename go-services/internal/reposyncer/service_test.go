package reposyncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestBuildCloneURL(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		fullPath string
		want     string
		wantErr  bool
	}{
		{
			name:     "simple path",
			baseURL:  "https://gitlab.example.com",
			fullPath: "group/project",
			want:     "https://gitlab.example.com/group/project.git",
		},
		{
			name:     "base URL with trailing slash",
			baseURL:  "https://gitlab.example.com/",
			fullPath: "group/project",
			want:     "https://gitlab.example.com/group/project.git",
		},
		{
			name:     "subgroup path",
			baseURL:  "https://gitlab.example.com",
			fullPath: "group/sub/project",
			want:     "https://gitlab.example.com/group/sub/project.git",
		},
		{
			name:     "base URL with subpath",
			baseURL:  "https://example.com/gitlab",
			fullPath: "group/project",
			want:     "https://example.com/gitlab/group/project.git",
		},
		{
			name:    "invalid URL",
			baseURL: ":",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildCloneURL(tc.baseURL, tc.fullPath)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// newTestSourceRepo creates a non-bare git repo with one commit on the default branch.
// Returns the repo path and initial HEAD SHA.
func newTestSourceRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()

	r, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}

	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}

	readmePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmePath, []byte("hello\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	sig := &object.Signature{
		Name:  "Test Author",
		Email: "test@example.com",
		When:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	hash, err := wt.Commit("initial commit", &gogit.CommitOptions{
		Author:    sig,
		Committer: sig,
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	return dir, hash.String()
}

// defaultBranch returns the short name of the HEAD branch in a repository.
func defaultBranch(t *testing.T, r *gogit.Repository) string {
	t.Helper()
	head, err := r.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	return head.Name().Short()
}

func TestSyncBareRepo_Clone(t *testing.T) {
	sourceDir, initialSHA := newTestSourceRepo(t)
	destDir := filepath.Join(t.TempDir(), "bare.git")

	r, err := syncBareRepo(context.Background(), destDir, sourceDir, "")
	if err != nil {
		t.Fatalf("syncBareRepo (clone): %v", err)
	}

	branch := defaultBranch(t, r)
	hash, err := r.ResolveRevision(plumbing.Revision("refs/heads/" + branch))
	if err != nil {
		t.Fatalf("ResolveRevision: %v", err)
	}
	if hash.String() != initialSHA {
		t.Errorf("head SHA = %s, want %s", hash, initialSHA)
	}
}

func TestSyncBareRepo_AlreadyUpToDate(t *testing.T) {
	sourceDir, _ := newTestSourceRepo(t)
	destDir := filepath.Join(t.TempDir(), "bare.git")

	// Initial clone.
	if _, err := syncBareRepo(context.Background(), destDir, sourceDir, ""); err != nil {
		t.Fatalf("syncBareRepo (initial): %v", err)
	}

	// Second call — no new commits, should handle NoErrAlreadyUpToDate gracefully.
	if _, err := syncBareRepo(context.Background(), destDir, sourceDir, ""); err != nil {
		t.Fatalf("syncBareRepo (fetch no-op): %v", err)
	}
}

func TestSyncBareRepo_Fetch(t *testing.T) {
	sourceDir, _ := newTestSourceRepo(t)
	destDir := filepath.Join(t.TempDir(), "bare.git")

	// Initial clone.
	r, err := syncBareRepo(context.Background(), destDir, sourceDir, "")
	if err != nil {
		t.Fatalf("syncBareRepo (initial): %v", err)
	}
	branch := defaultBranch(t, r)

	// Add a second commit to source.
	sourceRepo, err := gogit.PlainOpen(sourceDir)
	if err != nil {
		t.Fatalf("PlainOpen source: %v", err)
	}
	wt, err := sourceRepo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	extraPath := filepath.Join(sourceDir, "extra.txt")
	if err := os.WriteFile(extraPath, []byte("extra\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := wt.Add("extra.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	sig := &object.Signature{
		Name:  "Test Author",
		Email: "test@example.com",
		When:  time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	newHash, err := wt.Commit("second commit", &gogit.CommitOptions{
		Author:    sig,
		Committer: sig,
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Fetch.
	r, err = syncBareRepo(context.Background(), destDir, sourceDir, "")
	if err != nil {
		t.Fatalf("syncBareRepo (fetch): %v", err)
	}

	hash, err := r.ResolveRevision(plumbing.Revision("refs/heads/" + branch))
	if err != nil {
		t.Fatalf("ResolveRevision after fetch: %v", err)
	}
	if hash.String() != newHash.String() {
		t.Errorf("after fetch: head SHA = %s, want %s", hash, newHash)
	}
}

func TestResolveRevision_NonExistentBranch(t *testing.T) {
	sourceDir, _ := newTestSourceRepo(t)
	destDir := filepath.Join(t.TempDir(), "bare.git")

	r, err := syncBareRepo(context.Background(), destDir, sourceDir, "")
	if err != nil {
		t.Fatalf("syncBareRepo: %v", err)
	}

	_, err = r.ResolveRevision(plumbing.Revision("refs/heads/nonexistent-branch-xyz"))
	if err == nil {
		t.Error("expected error for non-existent branch, got nil")
	}
}
