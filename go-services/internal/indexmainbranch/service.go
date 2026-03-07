package indexmainbranch

import (
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	restate "github.com/restatedev/sdk-go"

	"ai-reviewer/go-services/internal/db"
	"ai-reviewer/go-services/internal/indexing"
	"ai-reviewer/go-services/internal/reposyncer"
)

// IndexMainBranch is a Restate Virtual Object that keeps the primary branch's
// vector index warm via a self-scheduling loop. Keyed by repo ID.
type IndexMainBranch struct {
	pool *pgxpool.Pool
}

// New creates a new IndexMainBranch virtual object.
func New(pool *pgxpool.Pool) *IndexMainBranch {
	return &IndexMainBranch{pool: pool}
}

// RunRequest is the input for Run.
type RunRequest struct {
	RepoID string `json:"repo_id"`
}

const rescheduleDelay = 6 * time.Hour

// Run syncs and indexes the primary branch, then schedules the next run.
func (s *IndexMainBranch) Run(ctx restate.ObjectContext, req RunRequest) (restate.Void, error) {
	repo, _, err := db.GetRepoWithProvider(ctx, s.pool, req.RepoID)
	if err != nil {
		log.Printf("IndexMainBranch: repo %s not found: %v", req.RepoID, err)
		return restate.Void{}, restate.TerminalError(fmt.Errorf("repo not found: %w", err), 404)
	}

	if !repo.ReviewEnabled {
		log.Printf("IndexMainBranch: repo %s has review disabled, stopping loop", req.RepoID)
		return restate.Void{}, nil
	}

	defaultBranch := repo.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	// Reschedule next run on successful completion.
	// Note: Early returns above (disabled review, up-to-date index) skip this defer.
	defer func() {
		restate.ObjectSend(ctx, "IndexMainBranch", req.RepoID, "Run").
			Send(RunRequest{RepoID: req.RepoID}, restate.WithDelay(rescheduleDelay))
	}()

	// Step 1: Sync repo.
	syncResult, err := restate.Service[reposyncer.SyncResult](ctx, "RepoSyncer", "SyncRepo").
		Request(reposyncer.SyncRequest{
			RepoID:       req.RepoID,
			TargetBranch: defaultBranch,
		})
	if err != nil {
		log.Printf("IndexMainBranch: SyncRepo failed for %s: %v", req.RepoID, err)
		return restate.Void{}, nil
	}

	// Step 2: Check if index is up to date.
	collectionName := indexing.SanitizeCollectionName(req.RepoID, defaultBranch)
	lastCommit, storedCollection, found, err := db.GetBranchIndex(ctx, s.pool, req.RepoID, defaultBranch)
	if err != nil {
		log.Printf("IndexMainBranch: reading branch index: %v", err)
	}
	if found && lastCommit == syncResult.HeadSHA {
		log.Printf("IndexMainBranch: branch index up to date for %s (sha=%s)", req.RepoID, syncResult.HeadSHA)
		_ = storedCollection
		return restate.Void{}, nil
	}

	// Step 3: Index the branch.
	var lastCommitPtr *string
	if found {
		lastCommitPtr = &lastCommit
	}
	idxResult, err := restate.Service[indexing.IndexResult](ctx, "Indexer", "IndexRepo").
		Request(indexing.IndexRequest{
			RepoID:            req.RepoID,
			RepoPath:          syncResult.RepoPath,
			Branch:            defaultBranch,
			HeadSHA:           syncResult.HeadSHA,
			CollectionName:    collectionName,
			LastIndexedCommit: lastCommitPtr,
		})
	if err != nil {
		log.Printf("IndexMainBranch: IndexRepo failed for %s: %v", req.RepoID, err)
		return restate.Void{}, nil
	}

	// Step 4: Update branch index.
	if err := db.UpsertBranchIndex(ctx, s.pool, req.RepoID, defaultBranch, syncResult.HeadSHA, idxResult.CollectionName); err != nil {
		log.Printf("IndexMainBranch: upserting branch index: %v", err)
	}

	log.Printf("IndexMainBranch: indexed %s branch %s (sha=%s, files=%d, chunks=%d)",
		req.RepoID, defaultBranch, syncResult.HeadSHA, idxResult.FilesIndexed, idxResult.ChunksUpserted)

	return restate.Void{}, nil
}
