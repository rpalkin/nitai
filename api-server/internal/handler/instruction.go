package handler

import (
	"context"
	"errors"
	"path/filepath"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ai-reviewer/api-server/internal/auth"
	"ai-reviewer/api-server/internal/db"
	apiv1 "ai-reviewer/gen/api/v1"
	"ai-reviewer/gen/api/v1/apiv1connect"
)

type InstructionHandler struct {
	apiv1connect.UnimplementedInstructionServiceHandler
	pool *pgxpool.Pool
}

func NewInstructionHandler(pool *pgxpool.Pool) *InstructionHandler {
	return &InstructionHandler{pool: pool}
}

func (h *InstructionHandler) CreateInstruction(ctx context.Context, req *connect.Request[apiv1.CreateInstructionRequest]) (*connect.Response[apiv1.CreateInstructionResponse], error) {
	orgID, ok := auth.OrgIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}

	content := req.Msg.Content
	if content == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("content is required"))
	}

	row, err := db.InsertInstruction(ctx, h.pool, orgID, req.Msg.Name, content, req.Msg.RepoFilter, req.Msg.FilePatternFilter)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&apiv1.CreateInstructionResponse{
		Instruction: instructionRowToProto(*row),
	}), nil
}

func (h *InstructionHandler) ListInstructions(ctx context.Context, req *connect.Request[apiv1.ListInstructionsRequest]) (*connect.Response[apiv1.ListInstructionsResponse], error) {
	orgID, ok := auth.OrgIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}

	rows, err := db.ListInstructionsByOrg(ctx, h.pool, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	instructions := make([]*apiv1.ReviewInstruction, len(rows))
	for i, row := range rows {
		instructions[i] = instructionRowToProto(row)
	}

	return connect.NewResponse(&apiv1.ListInstructionsResponse{
		Instructions: instructions,
	}), nil
}

func (h *InstructionHandler) UpdateInstruction(ctx context.Context, req *connect.Request[apiv1.UpdateInstructionRequest]) (*connect.Response[apiv1.UpdateInstructionResponse], error) {
	if req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("id is required"))
	}
	if req.Msg.Content == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("content is required"))
	}

	row, err := db.UpdateInstruction(ctx, h.pool, req.Msg.Id, req.Msg.Name, req.Msg.Content, req.Msg.RepoFilter, req.Msg.FilePatternFilter, req.Msg.Enabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("instruction not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&apiv1.UpdateInstructionResponse{
		Instruction: instructionRowToProto(*row),
	}), nil
}

func (h *InstructionHandler) DeleteInstruction(ctx context.Context, req *connect.Request[apiv1.DeleteInstructionRequest]) (*connect.Response[apiv1.DeleteInstructionResponse], error) {
	if req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("id is required"))
	}

	err := db.DeleteInstruction(ctx, h.pool, req.Msg.Id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("instruction not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&apiv1.DeleteInstructionResponse{}), nil
}

func (h *InstructionHandler) ResolveInstructions(ctx context.Context, req *connect.Request[apiv1.ResolveInstructionsRequest]) (*connect.Response[apiv1.ResolveInstructionsResponse], error) {
	orgID, ok := auth.OrgIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}

	if req.Msg.RepoId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("repo_id is required"))
	}

	rows, err := db.ListEnabledInstructionsByOrg(ctx, h.pool, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var matched []*apiv1.ReviewInstruction
	for _, row := range rows {
		if matchInstruction(row, req.Msg.RepoId, req.Msg.ChangedFiles) {
			matched = append(matched, instructionRowToProto(row))
		}
	}

	return connect.NewResponse(&apiv1.ResolveInstructionsResponse{
		Instructions: matched,
	}), nil
}

// matchInstruction checks if an instruction matches the given repo ID and changed files.
// An empty filter means "match all" for that dimension.
// If both filters are present, BOTH must match (AND logic).
func matchInstruction(instruction db.InstructionRow, repoID string, changedFiles []string) bool {
	// Check repo filter
	if len(instruction.RepoFilter) > 0 {
		found := false
		for _, id := range instruction.RepoFilter {
			if id == repoID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check file pattern filter
	if len(instruction.FilePatternFilter) > 0 {
		found := false
		for _, pattern := range instruction.FilePatternFilter {
			for _, file := range changedFiles {
				if matched, err := filepath.Match(pattern, file); err == nil && matched {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}
