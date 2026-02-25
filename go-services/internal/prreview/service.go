package prreview

import (
	"fmt"
	"log"
	"time"

	restate "github.com/restatedev/sdk-go"
	"github.com/jackc/pgx/v5/pgxpool"

	"ai-reviewer/go-services/internal/db"
	"ai-reviewer/go-services/internal/difffetcher"
	"ai-reviewer/go-services/internal/postreview"
)

// PRReview is a Restate Virtual Object that orchestrates the full PR review pipeline.
// It is keyed by "<repo_id>-<mr_number>" to ensure one active review per PR at a time.
type PRReview struct {
	pool *pgxpool.Pool
}

// New creates a new PRReview virtual object.
func New(pool *pgxpool.Pool) *PRReview {
	return &PRReview{pool: pool}
}

// RunRequest is the input for Run.
type RunRequest struct {
	RunID    string `json:"run_id"`
	RepoID   string `json:"repo_id"`
	MRNumber int    `json:"mr_number"`
	DryRun   bool   `json:"dry_run"`
	Force    bool   `json:"force"`
}

// reviewerInput is the payload sent to the Python Reviewer service.
type reviewerInput struct {
	Diff          string   `json:"diff"`
	MRTitle       string   `json:"mr_title"`
	MRDescription string   `json:"mr_description"`
	MRAuthor      string   `json:"mr_author"`
	SourceBranch  string   `json:"source_branch"`
	TargetBranch  string   `json:"target_branch"`
	ChangedFiles  []string `json:"changed_files"`
}

// reviewComment is a single inline comment from the Reviewer service.
type reviewComment struct {
	FilePath  string `json:"file_path"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Body      string `json:"body"`
}

// reviewerOutput is the response from the Python Reviewer service.
type reviewerOutput struct {
	Summary  string          `json:"summary"`
	Comments []reviewComment `json:"comments"`
}

// Run orchestrates the full PR review pipeline. Returns the review_run_id.
func (p *PRReview) Run(ctx restate.ObjectContext, req RunRequest) (string, error) {
	// Smart debounce: only delay when a recent invocation was cancelled (rapid push scenario).
	// First trigger for an MR proceeds immediately.
	lastStarted, _ := restate.Get[int64](ctx, "last_started_at")
	now := time.Now().UnixMilli()
	restate.Set(ctx, "last_started_at", now)

	if lastStarted > 0 && (now-lastStarted) < 3*60*1000 {
		// A recent invocation was cancelled — debounce before proceeding.
		if err := restate.Sleep(ctx, 3*time.Minute); err != nil {
			return "", err
		}
	}

	var runID string
	if req.RunID != "" {
		runID = req.RunID
	} else {
		id, err := db.CreateReviewRun(ctx, p.pool, req.RepoID, req.MRNumber)
		if err != nil {
			return "", fmt.Errorf("creating review run: %w", err)
		}
		runID = id
	}

	// fail updates the run status to failed and propagates the error.
	fail := func(err error) (string, error) {
		_ = db.UpdateReviewRunStatus(ctx, p.pool, runID, "failed")
		return "", err
	}

	// Step 1: Fetch diff + details from the VCS provider (includes dedup check).
	fetchResp, err := restate.Service[difffetcher.FetchResponse](ctx, "DiffFetcher", "FetchPRDetails").
		Request(difffetcher.FetchRequest{
			RepoID:   req.RepoID,
			MRNumber: req.MRNumber,
			Force:    req.Force,
		})
	if err != nil {
		return fail(fmt.Errorf("fetching PR details: %w", err))
	}

	// Step 2: Guard against race where MR became a draft during debounce.
	if fetchResp.Draft {
		log.Printf("PRReview: MR %d is draft, skipping", req.MRNumber)
		_ = db.UpdateReviewRunStatus(ctx, p.pool, runID, "draft")
		return runID, nil
	}

	// Step 3: Skip if diff hash matches a previous completed review.
	if fetchResp.Skip {
		if err := db.UpdateReviewRunStatus(ctx, p.pool, runID, "skipped"); err != nil {
			return "", fmt.Errorf("updating run status to skipped: %w", err)
		}
		return runID, nil
	}

	// Step 3: Persist diff hash for future dedup.
	if fetchResp.DiffHash != "" {
		if err := db.UpdateReviewRunDiffHash(ctx, p.pool, runID, fetchResp.DiffHash); err != nil {
			return fail(fmt.Errorf("storing diff hash: %w", err))
		}
	}

	// Step 4: Mark run as running.
	if err := db.UpdateReviewRunStatus(ctx, p.pool, runID, "running"); err != nil {
		return fail(fmt.Errorf("updating run status: %w", err))
	}

	// Step 5: Short-circuit if diff is too large to review.
	if fetchResp.DiffTooLarge {
		_, err := restate.Service[postreview.PostResponse](ctx, "PostReview", "Post").
			Request(postreview.PostRequest{
				ReviewRunID:  runID,
				RepoID:       req.RepoID,
				MRNumber:     req.MRNumber,
				RepoRemoteID: fetchResp.RepoRemoteID,
				Summary:      "This PR is too large to review automatically (> 5000 changed lines).",
				DryRun:       req.DryRun,
			})
		if err != nil {
			return fail(fmt.Errorf("posting too-large message: %w", err))
		}
		if err := db.UpdateReviewRunStatus(ctx, p.pool, runID, "completed"); err != nil {
			return fail(err)
		}
		return runID, nil
	}

	// Step 6: Call the Python Reviewer service (cross-language via Restate).
	reviewer, err := restate.Service[reviewerOutput](ctx, "Reviewer", "RunReview").
		Request(reviewerInput{
			Diff:          fetchResp.Diff,
			MRTitle:       fetchResp.MRTitle,
			MRDescription: fetchResp.MRDescription,
			MRAuthor:      fetchResp.MRAuthor,
			SourceBranch:  fetchResp.SourceBranch,
			TargetBranch:  fetchResp.TargetBranch,
			ChangedFiles:  fetchResp.ChangedFiles,
		})
	if err != nil {
		return fail(fmt.Errorf("running reviewer: %w", err))
	}

	// Step 7: Persist comments to DB before posting (idempotency).
	commentInputs := make([]db.ReviewCommentInput, len(reviewer.Comments))
	for i, c := range reviewer.Comments {
		commentInputs[i] = db.ReviewCommentInput{
			FilePath:  c.FilePath,
			LineStart: c.LineStart,
			LineEnd:   c.LineEnd,
			Body:      c.Body,
		}
	}
	if err := db.InsertReviewComments(ctx, p.pool, runID, commentInputs); err != nil {
		return fail(fmt.Errorf("inserting review comments: %w", err))
	}

	// Step 8: Post summary and inline comments to the provider.
	_, err = restate.Service[postreview.PostResponse](ctx, "PostReview", "Post").
		Request(postreview.PostRequest{
			ReviewRunID:  runID,
			RepoID:       req.RepoID,
			MRNumber:     req.MRNumber,
			RepoRemoteID: fetchResp.RepoRemoteID,
			Summary:      reviewer.Summary,
			DryRun:       req.DryRun,
		})
	if err != nil {
		return fail(fmt.Errorf("posting review: %w", err))
	}

	// Step 9: Mark run as completed.
	if err := db.UpdateReviewRunStatus(ctx, p.pool, runID, "completed"); err != nil {
		return fail(err)
	}

	return runID, nil
}
