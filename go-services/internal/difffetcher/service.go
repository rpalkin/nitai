package difffetcher

import (
	"errors"
	"fmt"

	restate "github.com/restatedev/sdk-go"
	"github.com/jackc/pgx/v5/pgxpool"

	"ai-reviewer/go-services/internal/crypto"
	"ai-reviewer/go-services/internal/db"
	"ai-reviewer/go-services/internal/provider"
	"ai-reviewer/go-services/internal/provider/gitlab"
)

const maxChangedLines = 5000

// DiffFetcher is a Restate service that fetches PR diff and details from the VCS provider.
type DiffFetcher struct {
	pool   *pgxpool.Pool
	encKey []byte
}

// New creates a new DiffFetcher.
func New(pool *pgxpool.Pool, encKey []byte) *DiffFetcher {
	return &DiffFetcher{pool: pool, encKey: encKey}
}

// FetchRequest is the input for FetchPRDetails.
type FetchRequest struct {
	RepoID   string `json:"repo_id"`
	MRNumber int    `json:"mr_number"`
	Force    bool   `json:"force"`
}

// FetchResponse is the output from FetchPRDetails.
type FetchResponse struct {
	Diff          string   `json:"diff"`
	MRTitle       string   `json:"mr_title"`
	MRDescription string   `json:"mr_description"`
	MRAuthor      string   `json:"mr_author"`
	SourceBranch  string   `json:"source_branch"`
	TargetBranch  string   `json:"target_branch"`
	ChangedFiles  []string `json:"changed_files"`
	ChangedLines  int      `json:"changed_lines"`
	DiffTooLarge  bool     `json:"diff_too_large"`
	RepoRemoteID  string   `json:"repo_remote_id"`
	DiffHash      string   `json:"diff_hash"`
	Skip          bool     `json:"skip"`
	Draft         bool     `json:"draft"`
}

// FetchPRDetails fetches the diff and metadata for a pull/merge request.
func (d *DiffFetcher) FetchPRDetails(ctx restate.Context, req FetchRequest) (FetchResponse, error) {
	repo, prov, err := db.GetRepoWithProvider(ctx, d.pool, req.RepoID)
	if err != nil {
		return FetchResponse{}, restate.TerminalError(fmt.Errorf("repo not found: %w", err), 404)
	}

	token, err := crypto.Decrypt(prov.TokenEncrypted, d.encKey)
	if err != nil {
		return FetchResponse{}, restate.TerminalError(fmt.Errorf("decrypting token: %w", err), 500)
	}

	client, err := newProvider(prov.Type, prov.BaseURL, string(token))
	if err != nil {
		return FetchResponse{}, restate.TerminalError(err, 400)
	}

	details, err := client.GetMRDetails(ctx, repo.RemoteID, req.MRNumber)
	if err != nil {
		return FetchResponse{}, classifyProviderError(err)
	}

	diffHash := details.HeadSHA

	if !req.Force {
		prevHash, found, err := db.GetLatestReviewDiffHash(ctx, d.pool, req.RepoID, req.MRNumber)
		if err != nil {
			return FetchResponse{}, fmt.Errorf("checking diff hash: %w", err)
		}
		if found && prevHash == diffHash {
			return FetchResponse{Skip: true, DiffHash: diffHash}, nil
		}
	}

	diff, err := client.GetMRDiff(ctx, repo.RemoteID, req.MRNumber)
	if err != nil {
		return FetchResponse{}, classifyProviderError(err)
	}

	changedFiles := make([]string, len(diff.ChangedFiles))
	for i, f := range diff.ChangedFiles {
		changedFiles[i] = f.NewPath
	}

	return FetchResponse{
		Diff:          diff.UnifiedDiff,
		MRTitle:       details.Title,
		MRDescription: details.Description,
		MRAuthor:      details.Author,
		SourceBranch:  details.SourceBranch,
		TargetBranch:  details.TargetBranch,
		ChangedFiles:  changedFiles,
		ChangedLines:  diff.ChangedLines,
		DiffTooLarge:  diff.ChangedLines > maxChangedLines,
		RepoRemoteID:  repo.RemoteID,
		DiffHash:      diffHash,
		Draft:         details.Draft,
	}, nil
}

func newProvider(provType, baseURL, token string) (provider.GitProvider, error) {
	switch provType {
	case "gitlab_self_hosted", "gitlab_cloud":
		if baseURL == "" {
			baseURL = "https://gitlab.com"
		}
		return gitlab.New(baseURL, token), nil
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", provType)
	}
}

func classifyProviderError(err error) error {
	switch {
	case errors.Is(err, provider.ErrNotFound):
		return restate.TerminalError(err, 404)
	case errors.Is(err, provider.ErrUnauthorized):
		return restate.TerminalError(err, 401)
	case errors.Is(err, provider.ErrForbidden):
		return restate.TerminalError(err, 403)
	default:
		// Retryable: rate limit, network errors, etc.
		return err
	}
}
