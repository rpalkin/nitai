package reposyncer

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/jackc/pgx/v5/pgxpool"
	restate "github.com/restatedev/sdk-go"

	"ai-reviewer/go-services/internal/crypto"
	"ai-reviewer/go-services/internal/db"
)

const reposBase = "/data/repos"

// RepoSyncer is a Restate service that maintains bare git clones on a shared volume.
type RepoSyncer struct {
	pool   *pgxpool.Pool
	encKey []byte
}

// New creates a new RepoSyncer.
func New(pool *pgxpool.Pool, encKey []byte) *RepoSyncer {
	return &RepoSyncer{pool: pool, encKey: encKey}
}

// SyncRequest is the input for SyncRepo.
type SyncRequest struct {
	RepoID       string `json:"repo_id"`
	TargetBranch string `json:"target_branch"`
}

// SyncResult is the output from SyncRepo.
type SyncResult struct {
	RepoPath string `json:"repo_path"` // /data/repos/<repo_id>
	HeadSHA  string `json:"head_sha"`  // SHA of HEAD at target_branch
}

// SyncRepo clones or fetches a bare git repository and returns the HEAD SHA for the target branch.
func (s *RepoSyncer) SyncRepo(ctx restate.Context, req SyncRequest) (SyncResult, error) {
	repo, prov, err := db.GetRepoWithProvider(ctx, s.pool, req.RepoID)
	if err != nil {
		return SyncResult{}, restate.TerminalError(fmt.Errorf("repo not found: %w", err), 404)
	}

	token, err := crypto.Decrypt(prov.TokenEncrypted, s.encKey)
	if err != nil {
		return SyncResult{}, restate.TerminalError(fmt.Errorf("decrypting token: %w", err), 500)
	}

	cloneURL, err := buildCloneURL(prov.BaseURL, repo.FullPath)
	if err != nil {
		return SyncResult{}, restate.TerminalError(fmt.Errorf("building clone URL: %w", err), 400)
	}

	repoPath := filepath.Join(reposBase, req.RepoID)
	gitRepo, err := syncBareRepo(ctx, repoPath, cloneURL, string(token))
	if err != nil {
		return SyncResult{}, fmt.Errorf("syncing repo: %w", err)
	}

	hash, err := gitRepo.ResolveRevision(plumbing.Revision("refs/heads/" + req.TargetBranch))
	if err != nil {
		return SyncResult{}, restate.TerminalError(
			fmt.Errorf("resolving branch %q: %w", req.TargetBranch, err), 404,
		)
	}

	return SyncResult{
		RepoPath: repoPath,
		HeadSHA:  hash.String(),
	}, nil
}

// syncBareRepo clones a bare repo at repoPath from cloneURL, or opens and fetches if the
// path already exists. token is empty for unauthenticated access (e.g. local paths in tests).
func syncBareRepo(ctx context.Context, repoPath, cloneURL, token string) (*gogit.Repository, error) {
	var auth transport.AuthMethod
	if token != "" {
		auth = &githttp.BasicAuth{Username: "oauth2", Password: token}
	}

	_, statErr := os.Stat(repoPath)
	switch {
	case os.IsNotExist(statErr):
		r, err := gogit.PlainClone(repoPath, true, &gogit.CloneOptions{
			URL:        cloneURL,
			Auth:       auth,
			NoCheckout: true,
		})
		if err != nil {
			return nil, fmt.Errorf("cloning repository: %w", err)
		}
		return r, nil
	case statErr != nil:
		return nil, fmt.Errorf("checking repo path: %w", statErr)
	}

	// Path exists — open and fetch.
	r, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("opening repository: %w", err)
	}

	// Update remote URL if it changed (e.g., after provider base URL migration).
	cfg, err := r.Config()
	if err != nil {
		return nil, fmt.Errorf("reading repo config: %w", err)
	}
	if remote, ok := cfg.Remotes["origin"]; ok {
		if len(remote.URLs) == 0 || remote.URLs[0] != cloneURL {
			remote.URLs = []string{cloneURL}
			if err := r.SetConfig(cfg); err != nil {
				return nil, fmt.Errorf("updating remote URL: %w", err)
			}
		}
	}

	err = r.FetchContext(ctx, &gogit.FetchOptions{
		Auth:     auth,
		Force:    true,
		RefSpecs: []gogitcfg.RefSpec{"+refs/heads/*:refs/heads/*"},
	})
	if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return nil, fmt.Errorf("fetching repository: %w", err)
	}

	return r, nil
}

// buildCloneURL constructs a HTTPS clone URL from a provider base URL and repo full path.
// Auth credentials are not embedded in the URL.
func buildCloneURL(baseURL, fullPath string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parsing base URL %q: %w", baseURL, err)
	}
	u.Path = path.Join(u.Path, fullPath) + ".git"
	return u.String(), nil
}
