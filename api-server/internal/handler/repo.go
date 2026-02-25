package handler

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apiv1 "ai-reviewer/gen/api/v1"
	"ai-reviewer/gen/api/v1/apiv1connect"
	"ai-reviewer/api-server/internal/db"
)

// RepoHandler implements apiv1connect.RepoServiceHandler.
type RepoHandler struct {
	apiv1connect.UnimplementedRepoServiceHandler
	pool *pgxpool.Pool
}

// NewRepoHandler creates a RepoHandler.
func NewRepoHandler(pool *pgxpool.Pool) *RepoHandler {
	return &RepoHandler{pool: pool}
}

// ListRepos returns all repositories for the given provider.
func (h *RepoHandler) ListRepos(ctx context.Context, req *connect.Request[apiv1.ListReposRequest]) (*connect.Response[apiv1.ListReposResponse], error) {
	if req.Msg.ProviderId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("provider_id is required"))
	}

	rows, err := db.ListReposByProvider(ctx, h.pool, req.Msg.ProviderId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing repos: %w", err))
	}

	repos := make([]*apiv1.Repository, len(rows))
	for i, r := range rows {
		repos[i] = repoRowToProto(r)
	}
	return connect.NewResponse(&apiv1.ListReposResponse{Repositories: repos}), nil
}

// EnableReview sets review_enabled=true on a repository.
func (h *RepoHandler) EnableReview(ctx context.Context, req *connect.Request[apiv1.EnableReviewRequest]) (*connect.Response[apiv1.EnableReviewResponse], error) {
	if req.Msg.RepoId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("repo_id is required"))
	}

	row, err := db.SetReviewEnabled(ctx, h.pool, req.Msg.RepoId, true)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("repository not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("enabling review: %w", err))
	}

	return connect.NewResponse(&apiv1.EnableReviewResponse{
		Repository: repoRowToProto(*row),
	}), nil
}

// DisableReview sets review_enabled=false on a repository.
func (h *RepoHandler) DisableReview(ctx context.Context, req *connect.Request[apiv1.DisableReviewRequest]) (*connect.Response[apiv1.DisableReviewResponse], error) {
	if req.Msg.RepoId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("repo_id is required"))
	}

	row, err := db.SetReviewEnabled(ctx, h.pool, req.Msg.RepoId, false)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("repository not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("disabling review: %w", err))
	}

	return connect.NewResponse(&apiv1.DisableReviewResponse{
		Repository: repoRowToProto(*row),
	}), nil
}
