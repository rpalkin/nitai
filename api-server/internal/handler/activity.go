package handler

import (
	"context"
	"encoding/json"
	"errors"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"ai-reviewer/api-server/internal/auth"
	"ai-reviewer/api-server/internal/db"
	apiv1 "ai-reviewer/gen/api/v1"
	"ai-reviewer/gen/api/v1/apiv1connect"
)

type ActivityHandler struct {
	apiv1connect.UnimplementedActivityServiceHandler
	pool *pgxpool.Pool
}

func NewActivityHandler(pool *pgxpool.Pool) *ActivityHandler {
	return &ActivityHandler{pool: pool}
}

func (h *ActivityHandler) ListActivityLogs(ctx context.Context, req *connect.Request[apiv1.ListActivityLogsRequest]) (*connect.Response[apiv1.ListActivityLogsResponse], error) {
	orgID, ok := auth.OrgIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}

	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	offset := int(req.Msg.Offset)
	if offset < 0 {
		offset = 0
	}

	var repoID *string
	if req.Msg.RepoId != "" {
		repoID = &req.Msg.RepoId
	}
	var eventType *string
	if req.Msg.EventType != "" {
		eventType = &req.Msg.EventType
	}

	logs, totalCount, err := db.ListActivityLogs(ctx, h.pool, orgID, repoID, eventType, limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoLogs := make([]*apiv1.ActivityLog, len(logs))
	for i, l := range logs {
		var repoIDStr string
		if l.RepoID != nil {
			repoIDStr = *l.RepoID
		}
		var actorIDStr string
		if l.ActorID != nil {
			actorIDStr = *l.ActorID
		}

		var details *structpb.Struct
		if len(l.Details) > 0 {
			var m map[string]interface{}
			if err := json.Unmarshal(l.Details, &m); err == nil {
				details, _ = structpb.NewStruct(m)
			}
		}
		if details == nil {
			details = &structpb.Struct{}
		}

		protoLogs[i] = &apiv1.ActivityLog{
			Id:        l.ID,
			OrgId:     l.OrgID,
			RepoId:    repoIDStr,
			ActorId:   actorIDStr,
			EventType: l.EventType,
			Details:   details,
			CreatedAt: timestamppb.New(l.CreatedAt),
		}
	}

	return connect.NewResponse(&apiv1.ListActivityLogsResponse{
		Logs:       protoLogs,
		TotalCount: int32(totalCount),
	}), nil
}
