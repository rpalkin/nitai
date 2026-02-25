package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"ai-reviewer/api-server/internal/db"
	"ai-reviewer/api-server/internal/handler"
	"ai-reviewer/api-server/internal/restate"
)

// stubWebhookStore is a test double for WebhookStore.
type stubWebhookStore struct {
	provider                *db.ProviderRow
	providerErr             error
	repo                    *db.RepoRow
	repoErr                 error
	activeInvocationID      *string
	activeInvocationErr     error
	createdRunID            string
	createRunErr            error
	draftRunID              string
	draftRunErr             error
	transitionErr           error
	// tracking
	createRunCalled      bool
	createDraftRunCalled bool
	transitionCalled     bool
}

func (s *stubWebhookStore) GetProvider(_ context.Context, _ string) (*db.ProviderRow, error) {
	return s.provider, s.providerErr
}

func (s *stubWebhookStore) GetRepoByRemoteID(_ context.Context, _, _ string) (*db.RepoRow, error) {
	return s.repo, s.repoErr
}

func (s *stubWebhookStore) GetActiveInvocationID(_ context.Context, _ string, _ int64) (*string, error) {
	return s.activeInvocationID, s.activeInvocationErr
}

func (s *stubWebhookStore) CreateReviewRunWithInvocation(_ context.Context, _ string, _ int64, _ string) (string, error) {
	s.createRunCalled = true
	return s.createdRunID, s.createRunErr
}

func (s *stubWebhookStore) CreateDraftReviewRun(_ context.Context, _ string, _ int64) (string, error) {
	s.createDraftRunCalled = true
	return s.draftRunID, s.draftRunErr
}

func (s *stubWebhookStore) TransitionDraftToReview(_ context.Context, _ string, _ int64) error {
	s.transitionCalled = true
	return s.transitionErr
}

// stubRestateDispatcher is a test double for RestateDispatcher.
type stubRestateDispatcher struct {
	invocationID    string
	sendErr         error
	cancelErr       error
	sendCalled      bool
	cancelCalled    bool
	cancelledIDs    []string
}

func (s *stubRestateDispatcher) SendPRReview(_ context.Context, _ string, _ restate.PRReviewRequest) (string, error) {
	s.sendCalled = true
	return s.invocationID, s.sendErr
}

func (s *stubRestateDispatcher) CancelInvocation(_ context.Context, invocationID string) error {
	s.cancelCalled = true
	s.cancelledIDs = append(s.cancelledIDs, invocationID)
	return s.cancelErr
}

func secret(s string) *string { return &s }
func strPtr(s string) *string { return &s }

func newWebhookRequest(method, path, token, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("X-Gitlab-Token", token)
	}
	r.Header.Set("Content-Type", "application/json")
	return r
}

const validPayload = `{"object_kind":"merge_request","object_attributes":{"action":"open","iid":42,"draft":false},"project":{"id":123}}`

func defaultProvider() *db.ProviderRow {
	return &db.ProviderRow{ID: "p1", WebhookSecret: secret("mysecret")}
}

func defaultRepo() *db.RepoRow {
	return &db.RepoRow{ID: "r1", ProviderID: "p1", RemoteID: "123", ReviewEnabled: true}
}

func TestWebhookHandler_ValidToken(t *testing.T) {
	store := &stubWebhookStore{
		provider:     defaultProvider(),
		repo:         defaultRepo(),
		createdRunID: "run1",
	}
	disp := &stubRestateDispatcher{invocationID: "inv1"}
	h := handler.NewWebhookHandler(store, disp)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "mysecret", validPayload))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestWebhookHandler_MissingToken(t *testing.T) {
	store := &stubWebhookStore{provider: defaultProvider()}
	h := handler.NewWebhookHandler(store, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "", validPayload))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestWebhookHandler_WrongToken(t *testing.T) {
	store := &stubWebhookStore{provider: defaultProvider()}
	h := handler.NewWebhookHandler(store, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "wrongtoken", validPayload))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestWebhookHandler_ProviderNotFound(t *testing.T) {
	store := &stubWebhookStore{providerErr: pgx.ErrNoRows}
	h := handler.NewWebhookHandler(store, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/nonexistent", "anytoken", validPayload))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestWebhookHandler_MethodNotAllowed(t *testing.T) {
	store := &stubWebhookStore{provider: defaultProvider()}
	h := handler.NewWebhookHandler(store, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newWebhookRequest(http.MethodGet, "/webhooks/p1", "mysecret", ""))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestWebhookHandler_NonMRObjectKind(t *testing.T) {
	store := &stubWebhookStore{provider: defaultProvider()}
	disp := &stubRestateDispatcher{}
	h := handler.NewWebhookHandler(store, disp)
	w := httptest.NewRecorder()
	payload := `{"object_kind":"push","project":{"id":123},"object_attributes":{}}`
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "mysecret", payload))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for non-MR event, got %d", w.Code)
	}
	if disp.sendCalled {
		t.Fatal("expected no dispatch for non-MR event")
	}
}

func TestWebhookHandler_ParsesMRPayload(t *testing.T) {
	store := &stubWebhookStore{
		provider:   &db.ProviderRow{ID: "p1", WebhookSecret: secret("s3cr3t")},
		repo:       &db.RepoRow{ID: "r1", ProviderID: "p1", RemoteID: "99", ReviewEnabled: true},
		draftRunID: "draft1",
	}
	h := handler.NewWebhookHandler(store, nil)
	w := httptest.NewRecorder()
	payload := `{"object_kind":"merge_request","object_attributes":{"action":"open","iid":7,"draft":true},"project":{"id":99}}`
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "s3cr3t", payload))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestWebhookHandler_MROpen_ReviewEnabled_Dispatches(t *testing.T) {
	store := &stubWebhookStore{
		provider:     defaultProvider(),
		repo:         defaultRepo(),
		createdRunID: "run1",
	}
	disp := &stubRestateDispatcher{invocationID: "inv1"}
	h := handler.NewWebhookHandler(store, disp)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "mysecret", validPayload))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !disp.sendCalled {
		t.Fatal("expected SendPRReview to be called")
	}
	if !store.createRunCalled {
		t.Fatal("expected CreateReviewRunWithInvocation to be called")
	}
}

func TestWebhookHandler_MROpen_ReviewDisabled_NoDispatch(t *testing.T) {
	repo := defaultRepo()
	repo.ReviewEnabled = false
	store := &stubWebhookStore{
		provider: defaultProvider(),
		repo:     repo,
	}
	disp := &stubRestateDispatcher{}
	h := handler.NewWebhookHandler(store, disp)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "mysecret", validPayload))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if disp.sendCalled {
		t.Fatal("expected no dispatch for review-disabled repo")
	}
}

func TestWebhookHandler_MROpen_UnknownRepo_NoDispatch(t *testing.T) {
	store := &stubWebhookStore{
		provider: defaultProvider(),
		repoErr:  pgx.ErrNoRows,
	}
	disp := &stubRestateDispatcher{}
	h := handler.NewWebhookHandler(store, disp)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "mysecret", validPayload))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if disp.sendCalled {
		t.Fatal("expected no dispatch for unknown repo")
	}
}

func TestWebhookHandler_DraftMR_NoDispatch(t *testing.T) {
	store := &stubWebhookStore{
		provider:   defaultProvider(),
		repo:       defaultRepo(),
		draftRunID: "draft1",
	}
	disp := &stubRestateDispatcher{}
	h := handler.NewWebhookHandler(store, disp)
	w := httptest.NewRecorder()
	payload := `{"object_kind":"merge_request","object_attributes":{"action":"open","iid":42,"draft":true},"project":{"id":123}}`
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "mysecret", payload))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if disp.sendCalled {
		t.Fatal("expected no dispatch for draft MR")
	}
	if !store.createDraftRunCalled {
		t.Fatal("expected CreateDraftReviewRun to be called")
	}
}

func TestWebhookHandler_DraftMR_CreatesDBRecord(t *testing.T) {
	store := &stubWebhookStore{
		provider:   defaultProvider(),
		repo:       defaultRepo(),
		draftRunID: "draft1",
	}
	disp := &stubRestateDispatcher{}
	h := handler.NewWebhookHandler(store, disp)
	w := httptest.NewRecorder()
	payload := `{"object_kind":"merge_request","object_attributes":{"action":"open","iid":42,"draft":true},"project":{"id":123}}`
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "mysecret", payload))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !store.createDraftRunCalled {
		t.Fatal("expected CreateDraftReviewRun to be called for draft MR open")
	}
	if disp.sendCalled {
		t.Fatal("expected no Restate dispatch for draft MR")
	}
}

func TestWebhookHandler_DraftMR_UpdateCreatesDBRecord(t *testing.T) {
	store := &stubWebhookStore{
		provider:   defaultProvider(),
		repo:       defaultRepo(),
		draftRunID: "draft2",
	}
	disp := &stubRestateDispatcher{}
	h := handler.NewWebhookHandler(store, disp)
	w := httptest.NewRecorder()
	// draft update — not a draft→ready transition
	payload := `{"object_kind":"merge_request","object_attributes":{"action":"update","iid":42,"draft":true},"project":{"id":123}}`
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "mysecret", payload))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !store.createDraftRunCalled {
		t.Fatal("expected CreateDraftReviewRun to be called for draft MR update")
	}
	if disp.sendCalled {
		t.Fatal("expected no Restate dispatch for draft MR update")
	}
}

func TestWebhookHandler_DraftToReadyTransition_Dispatches(t *testing.T) {
	store := &stubWebhookStore{
		provider:     defaultProvider(),
		repo:         defaultRepo(),
		createdRunID: "run1",
	}
	disp := &stubRestateDispatcher{invocationID: "inv1"}
	h := handler.NewWebhookHandler(store, disp)
	w := httptest.NewRecorder()
	// draft=true currently, but changes show draft went from true→false
	payload := `{"object_kind":"merge_request","object_attributes":{"action":"update","iid":42,"draft":false},"project":{"id":123},"changes":{"draft":{"previous":true,"current":false}}}`
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "mysecret", payload))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !disp.sendCalled {
		t.Fatal("expected dispatch for draft→ready transition")
	}
	if !store.transitionCalled {
		t.Fatal("expected TransitionDraftToReview to be called")
	}
}

func TestWebhookHandler_DraftToReady_TransitionsAndDispatches(t *testing.T) {
	store := &stubWebhookStore{
		provider:     defaultProvider(),
		repo:         defaultRepo(),
		createdRunID: "run1",
	}
	disp := &stubRestateDispatcher{invocationID: "inv1"}
	h := handler.NewWebhookHandler(store, disp)
	w := httptest.NewRecorder()
	payload := `{"object_kind":"merge_request","object_attributes":{"action":"update","iid":42,"draft":false},"project":{"id":123},"changes":{"draft":{"previous":true,"current":false}}}`
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "mysecret", payload))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !store.transitionCalled {
		t.Fatal("expected TransitionDraftToReview to be called")
	}
	if store.createDraftRunCalled {
		t.Fatal("expected CreateDraftReviewRun NOT to be called on draft→ready transition")
	}
	if !disp.sendCalled {
		t.Fatal("expected SendPRReview to be called")
	}
	if !store.createRunCalled {
		t.Fatal("expected CreateReviewRunWithInvocation to be called")
	}
}

func TestWebhookHandler_NonReviewableAction_NoDispatch(t *testing.T) {
	store := &stubWebhookStore{
		provider: defaultProvider(),
		repo:     defaultRepo(),
	}
	disp := &stubRestateDispatcher{}
	h := handler.NewWebhookHandler(store, disp)

	for _, action := range []string{"close", "merge", "approved"} {
		w := httptest.NewRecorder()
		payload := `{"object_kind":"merge_request","object_attributes":{"action":"` + action + `","iid":42,"draft":false},"project":{"id":123}}`
		h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "mysecret", payload))
		if w.Code != http.StatusOK {
			t.Fatalf("action=%s: expected 200, got %d", action, w.Code)
		}
		if disp.sendCalled {
			t.Fatalf("action=%s: expected no dispatch", action)
		}
	}
}

func TestWebhookHandler_CancelsExistingBeforeDispatch(t *testing.T) {
	existingInvID := "inv_old"
	store := &stubWebhookStore{
		provider:           defaultProvider(),
		repo:               defaultRepo(),
		activeInvocationID: strPtr(existingInvID),
		createdRunID:       "run1",
	}
	disp := &stubRestateDispatcher{invocationID: "inv_new"}
	h := handler.NewWebhookHandler(store, disp)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "mysecret", validPayload))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !disp.cancelCalled {
		t.Fatal("expected CancelInvocation to be called")
	}
	if len(disp.cancelledIDs) != 1 || disp.cancelledIDs[0] != existingInvID {
		t.Fatalf("expected cancel of %s, got %v", existingInvID, disp.cancelledIDs)
	}
	if !disp.sendCalled {
		t.Fatal("expected SendPRReview to be called after cancel")
	}
}

func TestWebhookHandler_CancelFails_StillDispatches(t *testing.T) {
	existingInvID := "inv_old"
	store := &stubWebhookStore{
		provider:           defaultProvider(),
		repo:               defaultRepo(),
		activeInvocationID: strPtr(existingInvID),
		createdRunID:       "run1",
	}
	disp := &stubRestateDispatcher{
		invocationID: "inv_new",
		cancelErr:    pgx.ErrNoRows, // simulate cancel failure
	}
	h := handler.NewWebhookHandler(store, disp)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newWebhookRequest(http.MethodPost, "/webhooks/p1", "mysecret", validPayload))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 even when cancel fails, got %d", w.Code)
	}
	if !disp.sendCalled {
		t.Fatal("expected SendPRReview still called after cancel error")
	}
}
