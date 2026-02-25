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
	"ai-reviewer/api-server/internal/restate"
)

// ReviewHandler implements apiv1connect.ReviewServiceHandler.
type ReviewHandler struct {
	apiv1connect.UnimplementedReviewServiceHandler
	pool    *pgxpool.Pool
	restate *restate.Client
}

// NewReviewHandler creates a ReviewHandler.
func NewReviewHandler(pool *pgxpool.Pool, restate *restate.Client) *ReviewHandler {
	return &ReviewHandler{pool: pool, restate: restate}
}

// TriggerReview creates a review run and sends a fire-and-forget message to Restate.
func (h *ReviewHandler) TriggerReview(ctx context.Context, req *connect.Request[apiv1.TriggerReviewRequest]) (*connect.Response[apiv1.TriggerReviewResponse], error) {
	msg := req.Msg
	if msg.RepoId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("repo_id is required"))
	}
	if msg.MrNumber <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("mr_number must be positive"))
	}

	// Verify repo exists.
	_, err := db.GetRepo(ctx, h.pool, msg.RepoId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("repository not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("getting repo: %w", err))
	}

	runID, err := db.CreateReviewRun(ctx, h.pool, msg.RepoId, msg.MrNumber)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("creating review run: %w", err))
	}

	key := fmt.Sprintf("%s-%d", msg.RepoId, msg.MrNumber)
	invocationID, err := h.restate.SendPRReview(ctx, key, restate.PRReviewRequest{
		RunID:    runID,
		RepoID:   msg.RepoId,
		MRNumber: msg.MrNumber,
		Force:    true,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sending to restate: %w", err))
	}

	if err := db.UpdateReviewRunInvocationID(ctx, h.pool, runID, invocationID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("storing invocation id: %w", err))
	}

	run, err := db.GetReviewRun(ctx, h.pool, runID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("fetching review run: %w", err))
	}

	return connect.NewResponse(&apiv1.TriggerReviewResponse{
		ReviewRun: reviewRunToProto(*run, nil),
	}), nil
}

// GetReviewRun fetches a review run with its comments.
func (h *ReviewHandler) GetReviewRun(ctx context.Context, req *connect.Request[apiv1.GetReviewRunRequest]) (*connect.Response[apiv1.GetReviewRunResponse], error) {
	if req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}

	run, err := db.GetReviewRun(ctx, h.pool, req.Msg.Id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("review run not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("getting review run: %w", err))
	}

	comments, err := db.GetReviewComments(ctx, h.pool, run.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("getting comments: %w", err))
	}

	return connect.NewResponse(&apiv1.GetReviewRunResponse{
		ReviewRun: reviewRunToProto(*run, comments),
	}), nil
}
