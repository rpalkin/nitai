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
	"ai-reviewer/go-services/internal/difffilter"
	"ai-reviewer/go-services/internal/indexing"
	"ai-reviewer/go-services/internal/instructions"
	"ai-reviewer/go-services/internal/localmerge"
	"ai-reviewer/go-services/internal/postreview"
	"ai-reviewer/go-services/internal/reporules"
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
	pool            *pgxpool.Pool
	debounceTimeout time.Duration
}

// Option is a functional option for PRReview.
type Option func(*PRReview)

// WithDebounceTimeout sets the debounce timeout for PRReview.
func WithDebounceTimeout(d time.Duration) Option {
	return func(p *PRReview) { p.debounceTimeout = d }
}

// New creates a new PRReview virtual object.
func New(pool *pgxpool.Pool, opts ...Option) *PRReview {
	p := &PRReview{pool: pool, debounceTimeout: 3 * time.Minute}
	for _, o := range opts {
		o(p)
	}
	return p
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
	Diff               string   `json:"diff"`
	MRTitle            string   `json:"mr_title"`
	MRDescription      string   `json:"mr_description"`
	MRAuthor           string   `json:"mr_author"`
	SourceBranch       string   `json:"source_branch"`
	TargetBranch       string   `json:"target_branch"`
	ChangedFiles       []string `json:"changed_files"`
	RepoPath           string   `json:"repo_path,omitempty"`
	MergeSHA           string   `json:"merge_sha,omitempty"`
	SearchCollection   string   `json:"search_collection,omitempty"`
	CustomInstructions []string `json:"custom_instructions,omitempty"`
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

	if lastStarted > 0 && lastCompleted != lastStarted && (now-lastStarted) < p.debounceTimeout.Milliseconds() {
		// A recent invocation was cancelled — debounce before proceeding.
		if err := restate.Sleep(ctx, p.debounceTimeout); err != nil {
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

	// Step 6: Sync the repository (clone or fetch bare git clone on shared volume).
	// Moved before DiffTooLarge check so we can read .review-rules.yaml for filtering.
	syncResult, err := restate.Service[reposyncer.SyncResult](ctx, "RepoSyncer", "SyncRepo").
		Request(reposyncer.SyncRequest{
			RepoID:       req.RepoID,
			TargetBranch: fetchResp.TargetBranch,
		})
	if err != nil {
		return fail(fmt.Errorf("syncing repo: %w", err))
	}

	// Step 6b: Compute local merge of source into target.
	// This produces the exact post-merge state for read_file and search indexing.
	// Falls back to source SHA if merge computation fails (e.g., SHA not in clone).
	mergeResult, err := restate.Run(ctx, func(restate.RunContext) (localmerge.Result, error) {
		res, mergeErr := localmerge.CreateMergeCommit(syncResult.RepoPath, syncResult.HeadSHA, fetchResp.DiffHash)
		if mergeErr != nil {
			return res, restate.TerminalError(mergeErr, 500)
		}
		return res, nil
	})
	if err != nil {
		log.Printf("PRReview: local merge failed, falling back to source SHA: %v", err)
	}
	if mergeResult.HasConflicts {
		log.Printf("PRReview: MR %d has merge conflicts, skipping", req.MRNumber)
		if err := db.UpdateReviewRunStatus(ctx, p.pool, runID, "conflicts"); err != nil {
			return "", fmt.Errorf("updating run status to conflicts: %w", err)
		}
		return runID, nil
	}
	toolsSHA := mergeResult.MergeSHA
	if toolsSHA == "" {
		toolsSHA = fetchResp.DiffHash // fallback: use source branch HEAD
	}

	// Step 7: Read .review-rules.yaml from bare clone at merge result commit.
	rules, err := restate.Run(ctx, func(restate.RunContext) (reporules.ReviewRules, error) {
		return reporules.ReadRepoRules(syncResult.RepoPath, toolsSHA, fetchResp.ChangedFiles)
	})
	if err != nil {
		// Non-fatal: log and proceed without rules
		log.Printf("PRReview: reading repo rules: %v", err)
		rules = reporules.ReviewRules{}
	}

	// Step 7b: Fetch org-level instructions from DB (non-fatal).
	orgInstructionRows, err := restate.Run(ctx, func(restate.RunContext) ([]db.InstructionRow, error) {
		return db.ResolveInstructionsForRepo(ctx, p.pool, req.RepoID)
	})
	if err != nil {
		log.Printf("PRReview: resolving org instructions: %v", err)
		orgInstructionRows = nil
	}

	// Step 8: Filter diff by ignore globs.
	diff := fetchResp.Diff
	changedFiles := fetchResp.ChangedFiles
	changedLines := fetchResp.ChangedLines
	if len(rules.IgnoreGlobs) > 0 {
		filtered := difffilter.FilterDiff(fetchResp.Diff, fetchResp.ChangedFiles, rules.IgnoreGlobs)
		if len(filtered.ChangedFiles) == 0 {
			log.Printf("PRReview: all files ignored by .review-rules.yaml, skipping")
			if err := db.UpdateReviewRunStatus(ctx, p.pool, runID, "skipped"); err != nil {
				return "", fmt.Errorf("updating run status to skipped: %w", err)
			}
			return runID, nil
		}
		diff = filtered.Diff
		changedFiles = filtered.ChangedFiles
		changedLines = filtered.ChangedLines
	}

	// Step 9: Short-circuit if diff is too large to review (evaluated after filtering).
	if changedLines > 5000 {
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

	// Step 10: Index the repository for semantic search (graceful degradation on failure).
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

	// Step 10b: Index merge result (clone target collection + incremental update).
	// Use source branch name for collection (one per PR branch, reused on re-push).
	mergeCollectionName := indexing.SanitizeCollectionName(req.RepoID, fetchResp.SourceBranch)
	if collectionName != "" {
		targetSHA := syncResult.HeadSHA
		mergeIdxResult, mergeErr := restate.Service[indexing.IndexResult](ctx, "Indexer", "IndexRepo").
			Request(indexing.IndexRequest{
				RepoID:             req.RepoID,
				RepoPath:           syncResult.RepoPath,
				Branch:             fetchResp.SourceBranch,
				HeadSHA:            toolsSHA,
				CollectionName:     mergeCollectionName,
				LastIndexedCommit:  &targetSHA,
				BaseCollectionName: collectionName,
			})
		if mergeErr != nil {
			log.Printf("PRReview: merge-result indexing failed, falling back to target collection: %v", mergeErr)
		} else {
			collectionName = mergeIdxResult.CollectionName
		}
	}

	// Step 11: Call the Python Reviewer service (cross-language via Restate).
	// Resolve org instructions using the filtered changed files, then merge with YAML instructions.
	mergedInstructions := instructions.Merge(
		instructions.Resolve(orgInstructionRows, req.RepoID, changedFiles),
		rules.Instructions,
	)
	reviewer, err := restate.Service[reviewerOutput](ctx, "Reviewer", "RunReview").
		Request(reviewerInput{
			Diff:               diff,
			MRTitle:            fetchResp.MRTitle,
			MRDescription:      fetchResp.MRDescription,
			MRAuthor:           fetchResp.MRAuthor,
			SourceBranch:       fetchResp.SourceBranch,
			TargetBranch:       fetchResp.TargetBranch,
			ChangedFiles:       changedFiles,
			RepoPath:           syncResult.RepoPath,
			MergeSHA:           toolsSHA,
			SearchCollection:   collectionName,
			CustomInstructions: mergedInstructions,
		})
	if err != nil {
		return fail(fmt.Errorf("running reviewer: %w", err))
	}

	// Step 12: Persist comments to DB before posting (idempotency).
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

	// Step 13: Post summary and inline comments to the provider (with rules_modified warning if applicable).
	summary := reviewer.Summary
	if rules.RulesModified {
		summary = "⚠️ **This PR modifies `.review-rules.yaml`.** Review rules have been adjusted accordingly.\n\n" + summary
	}
	_, err = restate.Service[postreview.PostResponse](ctx, "PostReview", "Post").
		Request(postreview.PostRequest{
			ReviewRunID:  runID,
			RepoID:       req.RepoID,
			MRNumber:     req.MRNumber,
			RepoRemoteID: fetchResp.RepoRemoteID,
			Summary:      summary,
			DryRun:       req.DryRun,
		})
	if err != nil {
		return fail(fmt.Errorf("posting review: %w", err))
	}

	// Step 14: Mark run as completed.
	if err := db.UpdateReviewRunStatus(ctx, p.pool, runID, "completed"); err != nil {
		return fail(err)
	}

	return runID, nil
}
