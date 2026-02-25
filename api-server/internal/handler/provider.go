package handler

import (
	"context"
	crypto_rand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apiv1 "ai-reviewer/gen/api/v1"
	"ai-reviewer/gen/api/v1/apiv1connect"
	"ai-reviewer/api-server/internal/crypto"
	"ai-reviewer/api-server/internal/db"
	"ai-reviewer/api-server/internal/provider/gitlab"
)

// insertProviderTx wraps InsertProvider + UpsertRepos in a single transaction.
func insertProviderTx(ctx context.Context, pool *pgxpool.Pool, orgID, provTypeStr, name, baseURL string, tokenEncrypted []byte, webhookSecret string, upsertInputs []db.RepoUpsertInput) (*db.ProviderRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	const q = `
		INSERT INTO providers (org_id, type, name, base_url, token_encrypted, webhook_secret)
		VALUES ($1, $2::provider_type, $3, $4, $5, $6)
		RETURNING id, org_id, type, name, base_url, token_encrypted, webhook_secret, created_at`

	row := &db.ProviderRow{}
	if err := tx.QueryRow(ctx, q, orgID, provTypeStr, name, baseURL, tokenEncrypted, webhookSecret).Scan(
		&row.ID, &row.OrgID, &row.Type, &row.Name, &row.BaseURL, &row.TokenEncrypted, &row.WebhookSecret, &row.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("insert provider: %w", err)
	}

	const uq = `
		INSERT INTO repositories (provider_id, remote_id, name, full_path)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (provider_id, remote_id) DO UPDATE
		SET name = EXCLUDED.name, full_path = EXCLUDED.full_path`

	for _, r := range upsertInputs {
		if _, err := tx.Exec(ctx, uq, row.ID, r.RemoteID, r.Name, r.FullPath); err != nil {
			return nil, fmt.Errorf("upsert repo: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}
	return row, nil
}

// ProviderHandler implements apiv1connect.ProviderServiceHandler.
type ProviderHandler struct {
	apiv1connect.UnimplementedProviderServiceHandler
	pool   *pgxpool.Pool
	encKey []byte
}

// NewProviderHandler creates a ProviderHandler.
func NewProviderHandler(pool *pgxpool.Pool, encKey []byte) *ProviderHandler {
	return &ProviderHandler{pool: pool, encKey: encKey}
}

// CreateProvider registers a new provider, syncs its repos, and returns the provider.
func (h *ProviderHandler) CreateProvider(ctx context.Context, req *connect.Request[apiv1.CreateProviderRequest]) (*connect.Response[apiv1.CreateProviderResponse], error) {
	msg := req.Msg
	if msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}
	if msg.Token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("token is required"))
	}
	provTypeStr := providerTypeToString(msg.Type)
	if provTypeStr == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported provider type"))
	}

	orgID, err := db.GetDefaultOrgID(ctx, h.pool)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("getting default org: %w", err))
	}

	tokenEncrypted, err := crypto.Encrypt([]byte(msg.Token), h.encKey)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encrypting token: %w", err))
	}

	// Fetch repos before writing to DB — so we can roll back atomically if it fails.
	baseURL := msg.BaseUrl
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}
	client := gitlab.New(baseURL, msg.Token)
	repos, err := client.ListRepos(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing repos: %w", err))
	}

	// Use a placeholder provider ID so we can build upsert inputs before the real INSERT.
	// The actual ID is assigned inside the transaction.
	upsertInputs := make([]db.RepoUpsertInput, len(repos))
	for i, r := range repos {
		upsertInputs[i] = db.RepoUpsertInput{
			// ProviderID is filled inside insertProviderTx after the INSERT.
			RemoteID: r.RemoteID,
			Name:     r.Name,
			FullPath: r.FullPath,
		}
	}

	secretBytes := make([]byte, 32)
	if _, err := crypto_rand.Read(secretBytes); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("generating webhook secret: %w", err))
	}
	webhookSecret := hex.EncodeToString(secretBytes)

	row, err := insertProviderTx(ctx, h.pool, orgID, provTypeStr, msg.Name, msg.BaseUrl, tokenEncrypted, webhookSecret, upsertInputs)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("creating provider: %w", err))
	}

	return connect.NewResponse(&apiv1.CreateProviderResponse{
		Provider:      providerRowToProto(*row),
		WebhookSecret: webhookSecret,
	}), nil
}

// ListProviders returns all active providers.
func (h *ProviderHandler) ListProviders(ctx context.Context, req *connect.Request[apiv1.ListProvidersRequest]) (*connect.Response[apiv1.ListProvidersResponse], error) {
	rows, err := db.ListProviders(ctx, h.pool)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing providers: %w", err))
	}

	providers := make([]*apiv1.Provider, len(rows))
	for i, r := range rows {
		providers[i] = providerRowToProto(r)
	}
	return connect.NewResponse(&apiv1.ListProvidersResponse{Providers: providers}), nil
}

// DeleteProvider soft-deletes a provider.
func (h *ProviderHandler) DeleteProvider(ctx context.Context, req *connect.Request[apiv1.DeleteProviderRequest]) (*connect.Response[apiv1.DeleteProviderResponse], error) {
	if req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}

	err := db.SoftDeleteProvider(ctx, h.pool, req.Msg.Id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("provider not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("deleting provider: %w", err))
	}

	return connect.NewResponse(&apiv1.DeleteProviderResponse{}), nil
}
