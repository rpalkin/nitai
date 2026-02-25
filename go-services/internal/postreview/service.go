package postreview

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

// PostReview is a Restate service that posts review results to the VCS provider.
type PostReview struct {
	pool   *pgxpool.Pool
	encKey []byte
}

// New creates a new PostReview service.
func New(pool *pgxpool.Pool, encKey []byte) *PostReview {
	return &PostReview{pool: pool, encKey: encKey}
}

// PostRequest is the input for Post.
type PostRequest struct {
	ReviewRunID  string `json:"review_run_id"`
	RepoID       string `json:"repo_id"`
	MRNumber     int    `json:"mr_number"`
	RepoRemoteID string `json:"repo_remote_id"`
	Summary      string `json:"summary"`
	DryRun       bool   `json:"dry_run"`
}

// PostResponse is the output from Post.
type PostResponse struct {
	CommentsPosted int  `json:"comments_posted"`
	SummaryPosted  bool `json:"summary_posted"`
}

// Post stores the summary and posts review comments to the VCS provider.
// In dry_run mode, the summary is stored but nothing is posted to the provider.
func (p *PostReview) Post(ctx restate.Context, req PostRequest) (PostResponse, error) {
	// Always persist the summary to DB.
	if err := db.UpdateReviewRunSummary(ctx, p.pool, req.ReviewRunID, req.Summary); err != nil {
		return PostResponse{}, fmt.Errorf("storing summary: %w", err)
	}

	if req.DryRun {
		return PostResponse{SummaryPosted: false}, nil
	}

	_, prov, err := db.GetRepoWithProvider(ctx, p.pool, req.RepoID)
	if err != nil {
		return PostResponse{}, restate.TerminalError(fmt.Errorf("repo not found: %w", err), 404)
	}

	token, err := crypto.Decrypt(prov.TokenEncrypted, p.encKey)
	if err != nil {
		return PostResponse{}, restate.TerminalError(fmt.Errorf("decrypting token: %w", err), 500)
	}

	client, err := newProvider(prov.Type, prov.BaseURL, string(token))
	if err != nil {
		return PostResponse{}, restate.TerminalError(err, 400)
	}

	// Post summary as a top-level MR note.
	if _, err := client.PostComment(ctx, req.RepoRemoteID, req.MRNumber, req.Summary); err != nil {
		return PostResponse{}, classifyProviderError(err)
	}

	// Load and post unposted inline comments. Already-posted ones are skipped on retry.
	comments, err := db.GetUnpostedComments(ctx, p.pool, req.ReviewRunID)
	if err != nil {
		return PostResponse{}, fmt.Errorf("loading unposted comments: %w", err)
	}

	posted := 0
	for _, c := range comments {
		result, err := client.PostInlineComment(ctx, req.RepoRemoteID, req.MRNumber, provider.InlineComment{
			FilePath: c.FilePath,
			Line:     c.LineStart,
			Body:     c.Body,
			NewLine:  true,
		})
		if err != nil {
			if errors.Is(err, provider.ErrInvalidInput) {
				// Invalid position (e.g. line not in diff) — skip and mark as posted to avoid
				// retrying a comment that will never succeed.
				if markErr := db.MarkCommentPosted(ctx, p.pool, c.ID, "skipped"); markErr != nil {
					return PostResponse{CommentsPosted: posted, SummaryPosted: true}, fmt.Errorf("marking skipped comment: %w", markErr)
				}
				continue
			}
			// Return partial progress — Restate will retry, and posted=true rows are skipped.
			return PostResponse{CommentsPosted: posted, SummaryPosted: true}, classifyProviderError(err)
		}
		if err := db.MarkCommentPosted(ctx, p.pool, c.ID, result.ID); err != nil {
			return PostResponse{CommentsPosted: posted, SummaryPosted: true}, fmt.Errorf("marking comment posted: %w", err)
		}
		posted++
	}

	return PostResponse{CommentsPosted: posted, SummaryPosted: true}, nil
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
		return err
	}
}
