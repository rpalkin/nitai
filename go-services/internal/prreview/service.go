package prreview

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	restate "github.com/restatedev/sdk-go"

	"ai-reviewer/go-services/internal/db"
	"ai-reviewer/go-services/internal/difffetcher"
	"ai-reviewer/go-services/internal/indexing"
	"ai-reviewer/go-services/internal/postreview"
	"ai-reviewer/go-services/internal/reposyncer"
)

// isCancellationError checks if an error represents an invocation cancellation.
// Cancellation errors should not update last_completed_at to allow debounce detection.
func isCancellationError(err error) bool {
	if err == nil {
		return false
	}
	// Check for context cancellation
	if errors.Is(err, context.Canceled) {
		return true
	}
	// Check for specific Restate cancellation error codes
	code := restate.ErrorCode(err)
	return code == 409 || code == 499 // 409 = Cancelled, 499 = Client Closed Request
}

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
	Diff             string   `json:"diff"`
	MRTitle          string   `json:"mr_title"`
	MRDescription    string   `json:"mr_description"`
	MRAuthor         string   `json:"mr_author"`
	SourceBranch     string   `json:"source_branch"`
	TargetBranch     string   `json:"target_branch"`
	ChangedFiles     []string `json:"changed_files"`
	RepoPath         string   `json:"repo_path,omitempty"`
	TargetBranchSHA  string   `json:"target_branch_sha,omitempty"`
	SearchCollection string   `json:"search_collection,omitempty"`
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
func (p *PRReview) Run(ctx restate.ObjectContext, req RunRequest) (runResult string, finalErr error) {
	// Smart debounce: only delay when a recent invocation was cancelled (rapid push scenario).
	// First trigger for an MR proceeds immediately. Completed invocations do not trigger debounce.
	lastStarted, _ := restate.Get[int64](ctx, "last_started_at")
	lastCompleted, _ := restate.Get[int64](ctx, "last_completed_at")
	now, err := restate.Run(ctx, func(restate.RunContext) (int64, error) {
		return time.Now().UnixMilli(), nil
	})
	if err != nil {
		return "", err
	}
	restate.Set(ctx, "last_started_at", now)

	// Mark this invocation as completed when it exits (success or error), so subsequent
	// invocations can distinguish a completion from a cancellation. Only skip on cancellation.
	defer func() {
		if finalErr == nil || !isCancellationError(finalErr) {
			restate.Set(ctx, "last_completed_at", now)
		}
	}()

	if lastStarted > 0 && lastCompleted != lastStarted && (now-lastStarted) < 3*60*1000 {
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
		if dbErr := db.UpdateReviewRunStatus(ctx, p.pool, runID, "failed"); dbErr != nil {
			log.Printf("PRReview: failed to update run status to failed: %v", dbErr)
		}
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
		if dbErr := db.UpdateReviewRunStatus(ctx, p.pool, runID, "draft"); dbErr != nil {
			log.Printf("PRReview: failed to update run status to draft: %v", dbErr)
		}
		return runID, nil
	}

	// Step 3: Skip if diff hash matches a previous completed review.
	if fetchResp.Skip {
		if err := db.UpdateReviewRunStatus(ctx, p.pool, runID, "skipped"); err != nil {
			return "", fmt.Errorf("updating run status to skipped: %w", err)
		}
		return runID, nil
	}

	// Step 4: Persist diff hash for future dedup.
	if fetchResp.DiffHash != "" {
		if err := db.UpdateReviewRunDiffHash(ctx, p.pool, runID, fetchResp.DiffHash); err != nil {
			return fail(fmt.Errorf("storing diff hash: %w", err))
		}
	}

	// Step 5: Mark run as running.
	if err := db.UpdateReviewRunStatus(ctx, p.pool, runID, "running"); err != nil {
		return fail(fmt.Errorf("updating run status: %w", err))
	}

	// Step 6: Short-circuit if diff is too large to review.
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

	// Step 7: Sync the repository (clone or fetch bare git clone on shared volume).
	syncResult, err := restate.Service[reposyncer.SyncResult](ctx, "RepoSyncer", "SyncRepo").
		Request(reposyncer.SyncRequest{
			RepoID:       req.RepoID,
			TargetBranch: fetchResp.TargetBranch,
		})
	if err != nil {
		return fail(fmt.Errorf("syncing repo: %w", err))
	}

	// Step 8: Index the repository for semantic search (graceful degradation on failure).
	collectionName := indexing.SanitizeCollectionName(req.RepoID, fetchResp.TargetBranch)
	lastCommit, storedCollection, found, err := db.GetBranchIndex(ctx, p.pool, req.RepoID, fetchResp.TargetBranch)
	if err != nil {
		log.Printf("PRReview: reading branch index: %v", err)
	}
	if found && lastCommit == syncResult.HeadSHA {
		log.Printf("PRReview: branch index up to date (sha=%s), skipping indexing", syncResult.HeadSHA)
		collectionName = storedCollection
	} else {
		var lastCommitPtr *string
		if found {
			lastCommitPtr = &lastCommit
		}
		idxResult, idxErr := restate.Service[indexing.IndexResult](ctx, "Indexer", "IndexRepo").
			Request(indexing.IndexRequest{
				RepoID:            req.RepoID,
				RepoPath:          syncResult.RepoPath,
				Branch:            fetchResp.TargetBranch,
				HeadSHA:           syncResult.HeadSHA,
				CollectionName:    collectionName,
				LastIndexedCommit: lastCommitPtr,
			})
		if idxErr != nil {
			log.Printf("PRReview: indexing failed, proceeding without search: %v", idxErr)
			collectionName = ""
		} else {
			if upsertErr := db.UpsertBranchIndex(ctx, p.pool, req.RepoID, fetchResp.TargetBranch, syncResult.HeadSHA, idxResult.CollectionName); upsertErr != nil {
				log.Printf("PRReview: upserting branch index: %v", upsertErr)
			}
			collectionName = idxResult.CollectionName
		}
	}

	// Step 9: Call the Python Reviewer service (cross-language via Restate).
	reviewer, err := restate.Service[reviewerOutput](ctx, "Reviewer", "RunReview").
		Request(reviewerInput{
			Diff:             fetchResp.Diff,
			MRTitle:          fetchResp.MRTitle,
			MRDescription:    fetchResp.MRDescription,
			MRAuthor:         fetchResp.MRAuthor,
			SourceBranch:     fetchResp.SourceBranch,
			TargetBranch:     fetchResp.TargetBranch,
			ChangedFiles:     fetchResp.ChangedFiles,
			RepoPath:         syncResult.RepoPath,
			TargetBranchSHA:  syncResult.HeadSHA,
			SearchCollection: collectionName,
		})
	if err != nil {
		return fail(fmt.Errorf("running reviewer: %w", err))
	}

	// Step 10: Persist comments to DB before posting (idempotency).
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

	// Step 11: Post summary and inline comments to the provider.
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

	// Step 12: Mark run as completed.
	if err := db.UpdateReviewRunStatus(ctx, p.pool, runID, "completed"); err != nil {
		return fail(err)
	}

	return runID, nil
}
