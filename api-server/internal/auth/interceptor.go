package auth

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
)

// Context keys for user_id and org_id
type contextKey string

const (
	ContextKeyUserID contextKey = "user_id"
	ContextKeyOrgID  contextKey = "org_id"
)

// UserIDFromContext extracts user_id from context
func UserIDFromContext(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(ContextKeyUserID).(string)
	return userID, ok
}

// OrgIDFromContext extracts org_id from context
func OrgIDFromContext(ctx context.Context) (string, bool) {
	orgID, ok := ctx.Value(ContextKeyOrgID).(string)
	return orgID, ok
}

// authAllowList contains procedures that don't require authentication
var authAllowList = map[string]bool{
	"/api.v1.AuthService/Register": true,
	"/api.v1.AuthService/Login":    true,
}

// NewAuthInterceptor creates a ConnectRPC unary interceptor for JWT authentication
func NewAuthInterceptor(jwtSecret string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			// Check if procedure is in allow-list
			if authAllowList[req.Spec().Procedure] {
				return next(ctx, req)
			}

			// Extract Authorization header
			authHeader := req.Header().Get("Authorization")
			if authHeader == "" {
				return nil, connect.NewError(connect.CodeUnauthenticated, nil)
			}

			// Parse Bearer token
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
				return nil, connect.NewError(connect.CodeUnauthenticated, nil)
			}
			tokenStr := parts[1]

			// Verify token
			claims, err := VerifyToken(tokenStr, jwtSecret)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, nil)
			}

			// Inject user_id and org_id into context
			ctx = context.WithValue(ctx, ContextKeyUserID, claims.UserID)
			ctx = context.WithValue(ctx, ContextKeyOrgID, claims.OrgID)

			return next(ctx, req)
		}
	}
}

// NewAuthInterceptorHTTP creates an HTTP middleware for JWT authentication
// This is a fallback for non-ConnectRPC handlers
func NewAuthInterceptorHTTP(jwtSecret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Parse Bearer token
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		tokenStr := parts[1]

		// Verify token
		claims, err := VerifyToken(tokenStr, jwtSecret)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Inject user_id and org_id into context
		ctx := context.WithValue(r.Context(), ContextKeyUserID, claims.UserID)
		ctx = context.WithValue(ctx, ContextKeyOrgID, claims.OrgID)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
