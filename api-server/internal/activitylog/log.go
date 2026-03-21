package activitylog

import (
	"context"
	"encoding/json"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"ai-reviewer/api-server/internal/db"
)

func Log(ctx context.Context, pool *pgxpool.Pool, orgID string, repoID, actorID *string, eventType string, details map[string]any) {
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		log.Printf("activitylog: marshal details: %v", err)
		return
	}
	if err := db.InsertActivityLog(ctx, pool, orgID, repoID, actorID, eventType, detailsJSON); err != nil {
		log.Printf("activitylog: insert: %v", err)
	}
}
