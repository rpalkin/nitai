package handler

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ai-reviewer/api-server/internal/db"
	"ai-reviewer/api-server/internal/restate"
)

// WebhookStore is the minimal DB interface needed by WebhookHandler.
type WebhookStore interface {
	GetProvider(ctx context.Context, id string) (*db.ProviderRow, error)
	GetRepoByRemoteID(ctx context.Context, providerID, remoteID string) (*db.RepoRow, error)
	GetActiveInvocationID(ctx context.Context, repoID string, mrNumber int64) (*string, error)
	CreateReviewRunWithInvocation(ctx context.Context, repoID string, mrNumber int64, invocationID string) (string, error)
	CreateDraftReviewRun(ctx context.Context, repoID string, mrNumber int64) (string, error)
	TransitionDraftToReview(ctx context.Context, repoID string, mrNumber int64) error
}

// RestateDispatcher abstracts Restate invocation submission and cancellation.
type RestateDispatcher interface {
	SendPRReview(ctx context.Context, key string, req restate.PRReviewRequest) (string, error)
	CancelInvocation(ctx context.Context, invocationID string) error
}

// PoolWebhookStore adapts *pgxpool.Pool to the WebhookStore interface.
type PoolWebhookStore struct {
	Pool *pgxpool.Pool
}

// GetProvider implements WebhookStore.
func (s *PoolWebhookStore) GetProvider(ctx context.Context, id string) (*db.ProviderRow, error) {
	return db.GetProvider(ctx, s.Pool, id)
}

// GetRepoByRemoteID implements WebhookStore.
func (s *PoolWebhookStore) GetRepoByRemoteID(ctx context.Context, providerID, remoteID string) (*db.RepoRow, error) {
	return db.GetRepoByRemoteID(ctx, s.Pool, providerID, remoteID)
}

// GetActiveInvocationID implements WebhookStore.
func (s *PoolWebhookStore) GetActiveInvocationID(ctx context.Context, repoID string, mrNumber int64) (*string, error) {
	return db.GetActiveInvocationID(ctx, s.Pool, repoID, mrNumber)
}

// CreateReviewRunWithInvocation implements WebhookStore.
func (s *PoolWebhookStore) CreateReviewRunWithInvocation(ctx context.Context, repoID string, mrNumber int64, invocationID string) (string, error) {
	return db.CreateReviewRunWithInvocation(ctx, s.Pool, repoID, mrNumber, invocationID)
}

// CreateDraftReviewRun implements WebhookStore.
func (s *PoolWebhookStore) CreateDraftReviewRun(ctx context.Context, repoID string, mrNumber int64) (string, error) {
	return db.CreateDraftReviewRun(ctx, s.Pool, repoID, mrNumber)
}

// TransitionDraftToReview implements WebhookStore.
func (s *PoolWebhookStore) TransitionDraftToReview(ctx context.Context, repoID string, mrNumber int64) error {
	return db.TransitionDraftToReview(ctx, s.Pool, repoID, mrNumber)
}

// GitLabWebhookPayload represents an incoming GitLab webhook payload.
type GitLabWebhookPayload struct {
	ObjectKind       string                `json:"object_kind"`
	Project          GitLabWebhookProject  `json:"project"`
	ObjectAttributes GitLabMRAttributes    `json:"object_attributes"`
	Changes          *GitLabWebhookChanges `json:"changes,omitempty"`
}

// GitLabWebhookProject holds the project info from a GitLab webhook.
type GitLabWebhookProject struct {
	ID int64 `json:"id"`
}

// GitLabMRAttributes holds merge request attributes from a GitLab webhook.
type GitLabMRAttributes struct {
	IID            int64  `json:"iid"`
	Action         string `json:"action"`
	Draft          bool   `json:"draft"`
	WorkInProgress bool   `json:"work_in_progress"`
}

// GitLabWebhookChanges holds changed fields from a GitLab webhook.
type GitLabWebhookChanges struct {
	Draft *GitLabFieldChange `json:"draft,omitempty"`
}

// GitLabFieldChange holds the previous and current value for a changed field.
type GitLabFieldChange struct {
	Previous any `json:"previous"`
	Current  any `json:"current"`
}

// WebhookHandler handles incoming GitLab webhook events.
type WebhookHandler struct {
	store      WebhookStore
	dispatcher RestateDispatcher
}

// NewWebhookHandler creates a WebhookHandler using the provided store and dispatcher.
func NewWebhookHandler(store WebhookStore, dispatcher RestateDispatcher) *WebhookHandler {
	return &WebhookHandler{store: store, dispatcher: dispatcher}
}

// ServeHTTP dispatches webhook requests routed to /webhooks/{provider_id}.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract provider_id from path: /webhooks/<provider_id>
	providerID := strings.TrimPrefix(r.URL.Path, "/webhooks/")
	providerID = strings.TrimSuffix(providerID, "/")
	if providerID == "" {
		http.Error(w, "provider id required", http.StatusNotFound)
		return
	}

	provider, err := h.store.GetProvider(r.Context(), providerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "provider not found", http.StatusNotFound)
			return
		}
		log.Printf("webhook: GetProvider(%s): %v", providerID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	token := r.Header.Get("X-Gitlab-Token")
	if token == "" || provider.WebhookSecret == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(*provider.WebhookSecret)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var payload GitLabWebhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	log.Printf("webhook: provider=%s object_kind=%s action=%s iid=%d project_id=%d draft=%v",
		providerID,
		payload.ObjectKind,
		payload.ObjectAttributes.Action,
		payload.ObjectAttributes.IID,
		payload.Project.ID,
		payload.ObjectAttributes.Draft || payload.ObjectAttributes.WorkInProgress,
	)

	// Filter non-MR events.
	if payload.ObjectKind != "merge_request" {
		log.Printf("webhook: ignoring non-MR event: %s", payload.ObjectKind)
		w.WriteHeader(http.StatusOK)
		return
	}

	action := payload.ObjectAttributes.Action
	mrIID := payload.ObjectAttributes.IID

	// Filter non-reviewable actions.
	reviewableActions := map[string]bool{"open": true, "update": true, "reopen": true}
	if !reviewableActions[action] {
		log.Printf("webhook: ignoring non-reviewable action: %s", action)
		w.WriteHeader(http.StatusOK)
		return
	}

	ctx := r.Context()
	remoteID := strconv.FormatInt(payload.Project.ID, 10)

	// Repo lookup (must happen before draft check to get repoID for DB calls).
	repo, err := h.store.GetRepoByRemoteID(ctx, providerID, remoteID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			log.Printf("webhook: repo not found for provider=%s remote_id=%s, ignoring", providerID, remoteID)
			w.WriteHeader(http.StatusOK)
			return
		}
		log.Printf("webhook: GetRepoByRemoteID: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !repo.ReviewEnabled {
		log.Printf("webhook: review disabled for repo=%s, ignoring", repo.ID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Draft detection.
	isDraft := payload.ObjectAttributes.Draft || payload.ObjectAttributes.WorkInProgress
	isDraftToReady := action == "update" && isDraftToReadyTransition(payload.Changes)

	if isDraft && !isDraftToReady {
		// Draft MR (open/update, not a transition): record it but don't dispatch.
		runID, err := h.store.CreateDraftReviewRun(ctx, repo.ID, mrIID)
		if err != nil {
			log.Printf("webhook: CreateDraftReviewRun: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Printf("webhook: draft MR %d recorded as run=%s, skipping dispatch", mrIID, runID)
		w.WriteHeader(http.StatusOK)
		return
	}

	if isDraftToReady {
		log.Printf("webhook: MR %d draft→ready transition, transitioning DB record", mrIID)
		if err := h.store.TransitionDraftToReview(ctx, repo.ID, mrIID); err != nil {
			log.Printf("webhook: TransitionDraftToReview: %v (continuing)", err)
		}
	}

	if h.dispatcher == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Cancel existing active invocation (best-effort).
	activeInvocationID, err := h.store.GetActiveInvocationID(ctx, repo.ID, mrIID)
	if err != nil {
		log.Printf("webhook: GetActiveInvocationID: %v", err)
	} else if activeInvocationID != nil {
		if err := h.dispatcher.CancelInvocation(ctx, *activeInvocationID); err != nil {
			log.Printf("webhook: CancelInvocation(%s): %v (continuing)", *activeInvocationID, err)
		} else {
			log.Printf("webhook: cancelled invocation %s for repo=%s mr=%d", *activeInvocationID, repo.ID, mrIID)
		}
	}

	// Submit new review invocation.
	key := fmt.Sprintf("%s-%d", repo.ID, mrIID)
	invocationID, err := h.dispatcher.SendPRReview(ctx, key, restate.PRReviewRequest{
		RepoID:   repo.ID,
		MRNumber: mrIID,
	})
	if err != nil {
		log.Printf("webhook: SendPRReview: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Create review run record.
	runID, err := h.store.CreateReviewRunWithInvocation(ctx, repo.ID, mrIID, invocationID)
	if err != nil {
		log.Printf("webhook: CreateReviewRunWithInvocation: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	log.Printf("webhook: dispatched review run=%s invocation=%s repo=%s mr=%d", runID, invocationID, repo.ID, mrIID)
	w.WriteHeader(http.StatusOK)
}

// isDraftToReadyTransition returns true if the changes indicate a draft→ready transition.
func isDraftToReadyTransition(changes *GitLabWebhookChanges) bool {
	if changes == nil || changes.Draft == nil {
		return false
	}
	prev, prevOk := changes.Draft.Previous.(bool)
	curr, currOk := changes.Draft.Current.(bool)
	return prevOk && currOk && prev && !curr
}
