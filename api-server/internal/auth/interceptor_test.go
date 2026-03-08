package auth

import (
	"context"
	"testing"
)

func TestUserIDFromContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), ContextKeyUserID, "user-123")
	userID, ok := UserIDFromContext(ctx)
	if !ok {
		t.Error("expected to find user_id in context")
	}
	if userID != "user-123" {
		t.Errorf("expected user_id=user-123, got %s", userID)
	}
}

func TestUserIDFromContext_NotFound(t *testing.T) {
	ctx := context.Background()
	_, ok := UserIDFromContext(ctx)
	if ok {
		t.Error("expected to not find user_id in empty context")
	}
}

func TestOrgIDFromContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), ContextKeyOrgID, "org-456")
	orgID, ok := OrgIDFromContext(ctx)
	if !ok {
		t.Error("expected to find org_id in context")
	}
	if orgID != "org-456" {
		t.Errorf("expected org_id=org-456, got %s", orgID)
	}
}

func TestOrgIDFromContext_NotFound(t *testing.T) {
	ctx := context.Background()
	_, ok := OrgIDFromContext(ctx)
	if ok {
		t.Error("expected to not find org_id in empty context")
	}
}
