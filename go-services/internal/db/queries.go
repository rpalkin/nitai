package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProviderRow holds provider data from the providers table.
type ProviderRow struct {
	ID             string
	Type           string
	BaseURL        string
	TokenEncrypted []byte
}

// RepoRow holds repository data from the repositories table.
type RepoRow struct {
	ID       string
	RemoteID string
	Name     string
	FullPath string
}

// ReviewCommentRow holds a review comment row from the database.
type ReviewCommentRow struct {
	ID          string
	ReviewRunID string
	FilePath    string
	LineStart   int
	LineEnd     int
	Body        string
}

// ReviewCommentInput holds data for inserting a new review comment.
type ReviewCommentInput struct {
	FilePath  string
	LineStart int
	LineEnd   int
	Body      string
}

// GetRepoWithProvider fetches a repository and its provider by repo ID.
func GetRepoWithProvider(ctx context.Context, pool *pgxpool.Pool, repoID string) (*RepoRow, *ProviderRow, error) {
	const q = `
		SELECT r.id, r.remote_id, r.name, r.full_path,
		       p.id, p.type, p.base_url, p.token_encrypted
		FROM repositories r
		JOIN providers p ON p.id = r.provider_id
		WHERE r.id = $1`

	var repo RepoRow
	var prov ProviderRow
	err := pool.QueryRow(ctx, q, repoID).Scan(
		&repo.ID, &repo.RemoteID, &repo.Name, &repo.FullPath,
		&prov.ID, &prov.Type, &prov.BaseURL, &prov.TokenEncrypted,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("GetRepoWithProvider: %w", err)
	}
	return &repo, &prov, nil
}

// CreateReviewRun inserts a new review run with status=pending and returns its ID.
func CreateReviewRun(ctx context.Context, pool *pgxpool.Pool, repoID string, mrNumber int) (string, error) {
	const q = `
		INSERT INTO review_runs (repo_id, mr_number, status)
		VALUES ($1, $2, 'pending')
		RETURNING id`

	var id string
	if err := pool.QueryRow(ctx, q, repoID, mrNumber).Scan(&id); err != nil {
		return "", fmt.Errorf("CreateReviewRun: %w", err)
	}
	return id, nil
}

// UpdateReviewRunStatus sets the status and updated_at of a review run.
func UpdateReviewRunStatus(ctx context.Context, pool *pgxpool.Pool, runID, status string) error {
	const q = `UPDATE review_runs SET status = $1, updated_at = now() WHERE id = $2`
	if _, err := pool.Exec(ctx, q, status, runID); err != nil {
		return fmt.Errorf("UpdateReviewRunStatus: %w", err)
	}
	return nil
}

// UpdateReviewRunSummary sets the summary and updated_at of a review run.
func UpdateReviewRunSummary(ctx context.Context, pool *pgxpool.Pool, runID, summary string) error {
	const q = `UPDATE review_runs SET summary = $1, updated_at = now() WHERE id = $2`
	if _, err := pool.Exec(ctx, q, summary, runID); err != nil {
		return fmt.Errorf("UpdateReviewRunSummary: %w", err)
	}
	return nil
}

// InsertReviewComments bulk-inserts review comments for a run (posted=false).
func InsertReviewComments(ctx context.Context, pool *pgxpool.Pool, runID string, comments []ReviewCommentInput) error {
	const q = `
		INSERT INTO review_comments (review_run_id, file_path, line_start, line_end, body, posted)
		VALUES ($1, $2, $3, $4, $5, false)`

	for _, c := range comments {
		if _, err := pool.Exec(ctx, q, runID, c.FilePath, c.LineStart, c.LineEnd, c.Body); err != nil {
			return fmt.Errorf("InsertReviewComments: %w", err)
		}
	}
	return nil
}

// GetUnpostedComments returns all comments for a run where posted=false, ordered by created_at.
func GetUnpostedComments(ctx context.Context, pool *pgxpool.Pool, runID string) ([]ReviewCommentRow, error) {
	const q = `
		SELECT id, review_run_id, file_path, line_start, line_end, body
		FROM review_comments
		WHERE review_run_id = $1 AND posted = false
		ORDER BY created_at`

	rows, err := pool.Query(ctx, q, runID)
	if err != nil {
		return nil, fmt.Errorf("GetUnpostedComments: %w", err)
	}
	defer rows.Close()

	var comments []ReviewCommentRow
	for rows.Next() {
		var c ReviewCommentRow
		if err := rows.Scan(&c.ID, &c.ReviewRunID, &c.FilePath, &c.LineStart, &c.LineEnd, &c.Body); err != nil {
			return nil, fmt.Errorf("GetUnpostedComments scan: %w", err)
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

// MarkCommentPosted sets posted=true and records the provider's comment ID.
func MarkCommentPosted(ctx context.Context, pool *pgxpool.Pool, commentID, providerCommentID string) error {
	const q = `UPDATE review_comments SET posted = true, provider_comment_id = $1 WHERE id = $2`
	if _, err := pool.Exec(ctx, q, providerCommentID, commentID); err != nil {
		return fmt.Errorf("MarkCommentPosted: %w", err)
	}
	return nil
}

// GetLatestReviewDiffHash returns the diff_hash of the most recent completed review
// for the given repo+MR, or ("", false, nil) if none exists.
func GetLatestReviewDiffHash(ctx context.Context, pool *pgxpool.Pool, repoID string, mrNumber int) (string, bool, error) {
	const q = `
		SELECT diff_hash FROM review_runs
		WHERE repo_id = $1 AND mr_number = $2 AND status = 'completed' AND diff_hash IS NOT NULL
		ORDER BY created_at DESC
		LIMIT 1`

	var hash string
	err := pool.QueryRow(ctx, q, repoID, mrNumber).Scan(&hash)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("GetLatestReviewDiffHash: %w", err)
	}
	return hash, true, nil
}

// UpdateReviewRunDiffHash sets the diff_hash and updated_at on a review run.
func UpdateReviewRunDiffHash(ctx context.Context, pool *pgxpool.Pool, runID, diffHash string) error {
	const q = `UPDATE review_runs SET diff_hash = $1, updated_at = now() WHERE id = $2`
	if _, err := pool.Exec(ctx, q, diffHash, runID); err != nil {
		return fmt.Errorf("UpdateReviewRunDiffHash: %w", err)
	}
	return nil
}
