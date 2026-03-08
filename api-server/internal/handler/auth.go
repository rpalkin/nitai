package handler

import (
	"context"
	"errors"
	"net/mail"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"ai-reviewer/api-server/internal/auth"
	"ai-reviewer/api-server/internal/db"
	apiv1 "ai-reviewer/gen/api/v1"
	"ai-reviewer/gen/api/v1/apiv1connect"
)

// AuthHandler implements the AuthService ConnectRPC service.
type AuthHandler struct {
	apiv1connect.UnimplementedAuthServiceHandler
	pool      *pgxpool.Pool
	jwtSecret string
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(pool *pgxpool.Pool, jwtSecret string) *AuthHandler {
	return &AuthHandler{
		pool:      pool,
		jwtSecret: jwtSecret,
	}
}

// Register implements AuthService.Register.
func (h *AuthHandler) Register(ctx context.Context, req *connect.Request[apiv1.RegisterRequest]) (*connect.Response[apiv1.RegisterResponse], error) {
	email := strings.TrimSpace(req.Msg.Email)
	password := req.Msg.Password

	// Validate email
	if email == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("email is required"))
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid email format"))
	}

	// Validate password
	if password == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("password is required"))
	}
	if len(password) < 8 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("password must be at least 8 characters"))
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to hash password"))
	}

	// Get default org
	orgID, err := db.GetDefaultOrgID(ctx, h.pool)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get default organization"))
	}

	// Insert user
	user, err := db.InsertUser(ctx, h.pool, orgID, email, string(hash))
	if err != nil {
		// Check for unique violation
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("email already registered"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create user"))
	}

	// Sign JWT
	token, err := auth.SignToken(user.ID, user.OrgID, h.jwtSecret)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to sign token"))
	}

	return connect.NewResponse(&apiv1.RegisterResponse{
		Token: token,
		User: &apiv1.User{
			Id:    user.ID,
			OrgId: user.OrgID,
			Email: user.Email,
		},
	}), nil
}

// Login implements AuthService.Login.
func (h *AuthHandler) Login(ctx context.Context, req *connect.Request[apiv1.LoginRequest]) (*connect.Response[apiv1.LoginResponse], error) {
	email := strings.TrimSpace(req.Msg.Email)
	password := req.Msg.Password

	// Validate input
	if email == "" || password == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid credentials"))
	}

	// Lookup user
	user, err := db.GetUserByEmail(ctx, h.pool, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid credentials"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to lookup user"))
	}

	// Compare password
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid credentials"))
	}

	// Sign JWT
	token, err := auth.SignToken(user.ID, user.OrgID, h.jwtSecret)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to sign token"))
	}

	return connect.NewResponse(&apiv1.LoginResponse{
		Token: token,
		User: &apiv1.User{
			Id:    user.ID,
			OrgId: user.OrgID,
			Email: user.Email,
		},
	}), nil
}

// GetMe implements AuthService.GetMe.
func (h *AuthHandler) GetMe(ctx context.Context, req *connect.Request[apiv1.GetMeRequest]) (*connect.Response[apiv1.GetMeResponse], error) {
	// Extract user_id from context (set by interceptor)
	userID, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}

	// Lookup user
	user, err := db.GetUserByID(ctx, h.pool, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("user not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to lookup user"))
	}

	return connect.NewResponse(&apiv1.GetMeResponse{
		User: &apiv1.User{
			Id:    user.ID,
			OrgId: user.OrgID,
			Email: user.Email,
		},
	}), nil
}
