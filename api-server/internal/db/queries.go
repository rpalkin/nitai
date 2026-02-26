package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProviderRow holds provider data from the providers table.
type ProviderRow struct {
	ID             string
	OrgID          string
	Type           string
	Name           string
	BaseURL        string
	TokenEncrypted []byte
	WebhookSecret  *string
	CreatedAt      time.Time
}

// RepoRow holds repository data from the repositories table.
type RepoRow struct {
	ID            string
	ProviderID    string
	RemoteID      string
	Name          string
	FullPath      string
	ReviewEnabled bool
	CreatedAt     time.Time
}

// RepoUpsertInput holds data for upserting a repository.
type RepoUpsertInput struct {
	ProviderID string
	RemoteID   string
	Name       string
	FullPath   string
}

// ReviewRunRow holds a review run row from the database.
type ReviewRunRow struct {
	ID                   string
	RepoID               string
	MRNumber             int64
	Status               string
	Summary              *string
	RestateInvocationID  *string
	CreatedAt            time.Time
	UpdatedAt            time.Time
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

// GetDefaultOrgID fetches the ID of the seeded 'default' organization.
func GetDefaultOrgID(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	const q = `SELECT id FROM organizations WHERE name = 'default' LIMIT 1`
	var id string
	if err := pool.QueryRow(ctx, q).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("default org not found")
		}
		return "", fmt.Errorf("GetDefaultOrgID: %w", err)
	}
	return id, nil
}

// InsertProvider inserts a new provider with an encrypted token and webhook secret, and returns the row.
func InsertProvider(ctx context.Context, pool *pgxpool.Pool, orgID, provType, name, baseURL string, tokenEncrypted []byte, webhookSecret string) (*ProviderRow, error) {
	const q = `
		INSERT INTO providers (org_id, type, name, base_url, token_encrypted, webhook_secret)
		VALUES ($1, $2::provider_type, $3, $4, $5, $6)
		RETURNING id, org_id, type, name, base_url, token_encrypted, webhook_secret, created_at`

	row := &ProviderRow{}
	err := pool.QueryRow(ctx, q, orgID, provType, name, baseURL, tokenEncrypted, webhookSecret).Scan(
		&row.ID, &row.OrgID, &row.Type, &row.Name, &row.BaseURL, &row.TokenEncrypted, &row.WebhookSecret, &row.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("InsertProvider: %w", err)
	}
	return row, nil
}

// ListProviders returns all active providers (no token_encrypted in SELECT).
func ListProviders(ctx context.Context, pool *pgxpool.Pool) ([]ProviderRow, error) {
	const q = `
		SELECT id, org_id, type, name, base_url, created_at
		FROM providers
		WHERE deleted_at IS NULL
		ORDER BY created_at`

	rows, err := pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("ListProviders: %w", err)
	}
	defer rows.Close()

	var providers []ProviderRow
	for rows.Next() {
		var p ProviderRow
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Type, &p.Name, &p.BaseURL, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("ListProviders scan: %w", err)
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

// GetProvider fetches a provider by ID (includes token and webhook_secret).
func GetProvider(ctx context.Context, pool *pgxpool.Pool, id string) (*ProviderRow, error) {
	const q = `
		SELECT id, org_id, type, name, base_url, token_encrypted, webhook_secret, created_at
		FROM providers
		WHERE id = $1 AND deleted_at IS NULL`

	row := &ProviderRow{}
	err := pool.QueryRow(ctx, q, id).Scan(
		&row.ID, &row.OrgID, &row.Type, &row.Name, &row.BaseURL, &row.TokenEncrypted, &row.WebhookSecret, &row.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("GetProvider: %w", err)
	}
	return row, nil
}

// SoftDeleteProvider sets deleted_at = now() for the provider.
func SoftDeleteProvider(ctx context.Context, pool *pgxpool.Pool, id string) error {
	const q = `UPDATE providers SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`
	tag, err := pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("SoftDeleteProvider: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// UpsertRepos batch-upserts repositories for a provider.
func UpsertRepos(ctx context.Context, pool *pgxpool.Pool, repos []RepoUpsertInput) error {
	const q = `
		INSERT INTO repositories (provider_id, remote_id, name, full_path)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (provider_id, remote_id) DO UPDATE
		SET name = EXCLUDED.name, full_path = EXCLUDED.full_path`

	for _, r := range repos {
		if _, err := pool.Exec(ctx, q, r.ProviderID, r.RemoteID, r.Name, r.FullPath); err != nil {
			return fmt.Errorf("UpsertRepos: %w", err)
		}
	}
	return nil
}

// ListReposByProvider returns all repositories for a given provider.
func ListReposByProvider(ctx context.Context, pool *pgxpool.Pool, providerID string) ([]RepoRow, error) {
	const q = `
		SELECT id, provider_id, remote_id, name, full_path, review_enabled, created_at
		FROM repositories
		WHERE provider_id = $1
		ORDER BY full_path`

	rows, err := pool.Query(ctx, q, providerID)
	if err != nil {
		return nil, fmt.Errorf("ListReposByProvider: %w", err)
	}
	defer rows.Close()

	var repos []RepoRow
	for rows.Next() {
		var r RepoRow
		if err := rows.Scan(&r.ID, &r.ProviderID, &r.RemoteID, &r.Name, &r.FullPath, &r.ReviewEnabled, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("ListReposByProvider scan: %w", err)
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

// GetRepo fetches a repository by ID.
func GetRepo(ctx context.Context, pool *pgxpool.Pool, id string) (*RepoRow, error) {
	const q = `
		SELECT id, provider_id, remote_id, name, full_path, review_enabled, created_at
		FROM repositories
		WHERE id = $1`

	row := &RepoRow{}
	err := pool.QueryRow(ctx, q, id).Scan(
		&row.ID, &row.ProviderID, &row.RemoteID, &row.Name, &row.FullPath, &row.ReviewEnabled, &row.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("GetRepo: %w", err)
	}
	return row, nil
}

// SetReviewEnabled updates review_enabled on a repository and returns the updated row.
func SetReviewEnabled(ctx context.Context, pool *pgxpool.Pool, id string, enabled bool) (*RepoRow, error) {
	const q = `
		UPDATE repositories SET review_enabled = $1
		WHERE id = $2
		RETURNING id, provider_id, remote_id, name, full_path, review_enabled, created_at`

	row := &RepoRow{}
	err := pool.QueryRow(ctx, q, enabled, id).Scan(
		&row.ID, &row.ProviderID, &row.RemoteID, &row.Name, &row.FullPath, &row.ReviewEnabled, &row.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("SetReviewEnabled: %w", err)
	}
	return row, nil
}

// CreateReviewRun inserts a new review run with status=pending and returns its ID.
func CreateReviewRun(ctx context.Context, pool *pgxpool.Pool, repoID string, mrNumber int64) (string, error) {
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

// GetReviewRun fetches a review run by ID.
func GetReviewRun(ctx context.Context, pool *pgxpool.Pool, id string) (*ReviewRunRow, error) {
	const q = `
		SELECT id, repo_id, mr_number, status, summary, restate_invocation_id, created_at, updated_at
		FROM review_runs
		WHERE id = $1`

	row := &ReviewRunRow{}
	err := pool.QueryRow(ctx, q, id).Scan(
		&row.ID, &row.RepoID, &row.MRNumber, &row.Status, &row.Summary, &row.RestateInvocationID, &row.CreatedAt, &row.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("GetReviewRun: %w", err)
	}
	return row, nil
}

// GetRepoByRemoteID looks up a repository by provider_id and remote_id.
func GetRepoByRemoteID(ctx context.Context, pool *pgxpool.Pool, providerID, remoteID string) (*RepoRow, error) {
	const q = `
		SELECT id, provider_id, remote_id, name, full_path, review_enabled, created_at
		FROM repositories
		WHERE provider_id = $1 AND remote_id = $2`

	row := &RepoRow{}
	err := pool.QueryRow(ctx, q, providerID, remoteID).Scan(
		&row.ID, &row.ProviderID, &row.RemoteID, &row.Name, &row.FullPath, &row.ReviewEnabled, &row.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("GetRepoByRemoteID: %w", err)
	}
	return row, nil
}

// GetActiveInvocationID returns the restate_invocation_id of the most recent pending/running review run for the given repo+MR.
func GetActiveInvocationID(ctx context.Context, pool *pgxpool.Pool, repoID string, mrNumber int64) (*string, error) {
	const q = `
		SELECT restate_invocation_id
		FROM review_runs
		WHERE repo_id = $1 AND mr_number = $2 AND status IN ('pending', 'running')
		ORDER BY created_at DESC
		LIMIT 1`

	var invocationID *string
	err := pool.QueryRow(ctx, q, repoID, mrNumber).Scan(&invocationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("GetActiveInvocationID: %w", err)
	}
	return invocationID, nil
}

// CreateReviewRunWithInvocation inserts a review run with a Restate invocation ID and returns its ID.
func CreateReviewRunWithInvocation(ctx context.Context, pool *pgxpool.Pool, repoID string, mrNumber int64, invocationID string) (string, error) {
	const q = `
		INSERT INTO review_runs (repo_id, mr_number, status, restate_invocation_id)
		VALUES ($1, $2, 'pending', $3)
		RETURNING id`

	var id string
	if err := pool.QueryRow(ctx, q, repoID, mrNumber, invocationID).Scan(&id); err != nil {
		return "", fmt.Errorf("CreateReviewRunWithInvocation: %w", err)
	}
	return id, nil
}

// CreateDraftReviewRun inserts a new review run with status=draft and returns its ID.
func CreateDraftReviewRun(ctx context.Context, pool *pgxpool.Pool, repoID string, mrNumber int64) (string, error) {
	const q = `
		INSERT INTO review_runs (repo_id, mr_number, status)
		VALUES ($1, $2, 'draft')
		RETURNING id`

	var id string
	if err := pool.QueryRow(ctx, q, repoID, mrNumber).Scan(&id); err != nil {
		return "", fmt.Errorf("CreateDraftReviewRun: %w", err)
	}
	return id, nil
}

// TransitionDraftToReview updates the most recent draft row for this repo+MR to status=pending.
// No-op if no draft row exists.
func TransitionDraftToReview(ctx context.Context, pool *pgxpool.Pool, repoID string, mrNumber int64) error {
	const q = `
		UPDATE review_runs
		SET status = 'pending'
		WHERE id = (
			SELECT id FROM review_runs
			WHERE repo_id = $1 AND mr_number = $2 AND status = 'draft'
			ORDER BY created_at DESC
			LIMIT 1
		)`

	_, err := pool.Exec(ctx, q, repoID, mrNumber)
	if err != nil {
		return fmt.Errorf("TransitionDraftToReview: %w", err)
	}
	return nil
}

// UpdateReviewRunInvocationID sets the restate_invocation_id on an existing review run.
func UpdateReviewRunInvocationID(ctx context.Context, pool *pgxpool.Pool, runID, invocationID string) error {
	const q = `UPDATE review_runs SET restate_invocation_id = $1 WHERE id = $2`
	_, err := pool.Exec(ctx, q, invocationID, runID)
	if err != nil {
		return fmt.Errorf("UpdateReviewRunInvocationID: %w", err)
	}
	return nil
}

// GetReviewComments returns all comments for a review run.
func GetReviewComments(ctx context.Context, pool *pgxpool.Pool, reviewRunID string) ([]ReviewCommentRow, error) {
	const q = `
		SELECT id, review_run_id, file_path, line_start, line_end, body
		FROM review_comments
		WHERE review_run_id = $1
		ORDER BY created_at`

	rows, err := pool.Query(ctx, q, reviewRunID)
	if err != nil {
		return nil, fmt.Errorf("GetReviewComments: %w", err)
	}
	defer rows.Close()

	var comments []ReviewCommentRow
	for rows.Next() {
		var c ReviewCommentRow
		if err := rows.Scan(&c.ID, &c.ReviewRunID, &c.FilePath, &c.LineStart, &c.LineEnd, &c.Body); err != nil {
			return nil, fmt.Errorf("GetReviewComments scan: %w", err)
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}
