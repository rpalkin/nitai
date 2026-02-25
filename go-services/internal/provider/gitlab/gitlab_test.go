package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"ai-reviewer/go-services/internal/provider"
)

// newTestServer creates an httptest server with the given handler map.
// Keys are paths (e.g. "/api/v4/projects"); values are http.HandlerFunc.
func newTestServer(t *testing.T, routes map[string]http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	mux := http.NewServeMux()
	for path, h := range routes {
		mux.HandleFunc(path, h)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(srv.URL, "test-token", WithHTTPClient(srv.Client()))
	return srv, c
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ── ListRepos ─────────────────────────────────────────────────────────────────

func TestListRepos_SinglePage(t *testing.T) {
	projects := []gitlabProject{
		{ID: 1, Name: "foo", PathWithNamespace: "ns/foo", HTTPURLToRepo: "https://gl.example/ns/foo.git"},
	}
	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects": func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// no X-Next-Page → single page
			writeJSON(w, projects)
		},
	})

	repos, err := c.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
	r := repos[0]
	if r.RemoteID != "1" || r.Name != "foo" || r.FullPath != "ns/foo" || r.HTTPURL != "https://gl.example/ns/foo.git" {
		t.Errorf("unexpected repo fields: %+v", r)
	}
}

func TestListRepos_MultiPage(t *testing.T) {
	page1 := []gitlabProject{{ID: 1, Name: "a"}}
	page2 := []gitlabProject{{ID: 2, Name: "b"}}

	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects": func(w http.ResponseWriter, r *http.Request) {
			pg := r.URL.Query().Get("page")
			switch pg {
			case "1", "":
				w.Header().Set("X-Next-Page", "2")
				writeJSON(w, page1)
			case "2":
				// no X-Next-Page
				writeJSON(w, page2)
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		},
	})

	repos, err := c.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}
}

func TestListRepos_Empty(t *testing.T) {
	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []gitlabProject{})
		},
	})

	repos, err := c.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("expected 0 repos, got %d", len(repos))
	}
}

func TestListRepos_Unauthorized(t *testing.T) {
	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})

	_, err := c.ListRepos(context.Background())
	if err != provider.ErrUnauthorized {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

// ── GetMRDetails ──────────────────────────────────────────────────────────────

func TestGetMRDetails_Success(t *testing.T) {
	mr := gitlabMR{
		Title:        "my MR",
		Description:  "desc",
		SourceBranch: "feature",
		TargetBranch: "main",
		SHA:          "abc123",
	}
	mr.Author.Username = "alice"

	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects/42/merge_requests/7": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, mr)
		},
	})

	got, err := c.GetMRDetails(context.Background(), "42", 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Title != "my MR" || got.Author != "alice" || got.HeadSHA != "abc123" {
		t.Errorf("unexpected details: %+v", got)
	}
	if got.SourceBranch != "feature" || got.TargetBranch != "main" {
		t.Errorf("unexpected branches: %+v", got)
	}
}

func TestGetMRDetails_NotFound(t *testing.T) {
	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects/42/merge_requests/99": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
	})

	_, err := c.GetMRDetails(context.Background(), "42", 99)
	if err != provider.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestGetMRDetails_DraftField(t *testing.T) {
	mr := gitlabMR{
		Title: "Draft MR",
		SHA:   "deadbeef",
		Draft: true,
	}
	mr.Author.Username = "bob"

	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects/10/merge_requests/3": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, mr)
		},
	})

	got, err := c.GetMRDetails(context.Background(), "10", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Draft {
		t.Errorf("expected Draft=true, got false")
	}
}

// ── GetMRDiff ─────────────────────────────────────────────────────────────────

func TestGetMRDiff_Success(t *testing.T) {
	changes := gitlabMRChanges{
		Changes: []gitlabDiffChange{
			{
				OldPath: "src/foo.go",
				NewPath: "src/foo.go",
				Diff:    "@@ -1,3 +1,4 @@\n context\n+added line\n-removed line\n context2\n",
			},
		},
	}
	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects/1/merge_requests/2/changes": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, changes)
		},
	})

	diff, err := c.GetMRDiff(context.Background(), "1", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diff.ChangedFiles) != 1 {
		t.Errorf("expected 1 changed file, got %d", len(diff.ChangedFiles))
	}
	if diff.ChangedLines != 2 { // 1 '+' and 1 '-'
		t.Errorf("expected 2 changed lines, got %d", diff.ChangedLines)
	}
	// Verify diff --git header is present.
	if !contains(diff.UnifiedDiff, "diff --git a/src/foo.go b/src/foo.go") {
		t.Errorf("unified diff header missing:\n%s", diff.UnifiedDiff)
	}
	if !contains(diff.UnifiedDiff, "--- a/src/foo.go") {
		t.Errorf("--- header missing:\n%s", diff.UnifiedDiff)
	}
	if !contains(diff.UnifiedDiff, "+++ b/src/foo.go") {
		t.Errorf("+++ header missing:\n%s", diff.UnifiedDiff)
	}
}

func TestGetMRDiff_NewFile(t *testing.T) {
	changes := gitlabMRChanges{
		Changes: []gitlabDiffChange{
			{
				OldPath: "newfile.txt",
				NewPath: "newfile.txt",
				NewFile: true,
				Diff:    "@@ -0,0 +1 @@\n+hello\n",
			},
		},
	}
	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects/1/merge_requests/3/changes": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, changes)
		},
	})

	diff, err := c.GetMRDiff(context.Background(), "1", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(diff.UnifiedDiff, "--- /dev/null") {
		t.Errorf("expected /dev/null for new file, got:\n%s", diff.UnifiedDiff)
	}
	if !contains(diff.UnifiedDiff, "new file mode") {
		t.Errorf("expected 'new file mode' header:\n%s", diff.UnifiedDiff)
	}
	if !diff.ChangedFiles[0].NewFile {
		t.Error("expected ChangedFile.NewFile=true")
	}
}

func TestGetMRDiff_DeletedFile(t *testing.T) {
	changes := gitlabMRChanges{
		Changes: []gitlabDiffChange{
			{
				OldPath:     "gone.txt",
				NewPath:     "gone.txt",
				DeletedFile: true,
				Diff:        "@@ -1 +0,0 @@\n-bye\n",
			},
		},
	}
	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects/1/merge_requests/4/changes": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, changes)
		},
	})

	diff, err := c.GetMRDiff(context.Background(), "1", 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(diff.UnifiedDiff, "+++ /dev/null") {
		t.Errorf("expected /dev/null for deleted file, got:\n%s", diff.UnifiedDiff)
	}
	if !diff.ChangedFiles[0].Deleted {
		t.Error("expected ChangedFile.Deleted=true")
	}
}

func TestGetMRDiff_NotFound(t *testing.T) {
	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects/1/merge_requests/99/changes": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
	})

	_, err := c.GetMRDiff(context.Background(), "1", 99)
	if err != provider.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ── PostComment ───────────────────────────────────────────────────────────────

func TestPostComment_Success(t *testing.T) {
	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects/5/merge_requests/1/notes": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			var req map[string]string
			json.NewDecoder(r.Body).Decode(&req)
			if req["body"] != "hello world" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, gitlabNote{ID: 42})
		},
	})

	result, err := c.PostComment(context.Background(), "5", 1, "hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != strconv.Itoa(42) {
		t.Errorf("expected ID=42, got %s", result.ID)
	}
}

func TestPostComment_Forbidden(t *testing.T) {
	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects/5/merge_requests/1/notes": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		},
	})

	_, err := c.PostComment(context.Background(), "5", 1, "body")
	if err != provider.ErrForbidden {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

// ── PostInlineComment ─────────────────────────────────────────────────────────

func versionsHandler(versions []gitlabMRVersion) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, versions)
	}
}

func discussionHandler(expectNewLine bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		pos, _ := payload["position"].(map[string]any)
		if expectNewLine {
			if _, ok := pos["new_line"]; !ok {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		} else {
			if _, ok := pos["old_line"]; !ok {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, gitlabDiscussion{ID: "disc-1"})
	}
}

func TestPostInlineComment_NewLine(t *testing.T) {
	versions := []gitlabMRVersion{{ID: 1, HeadSHA: "head", BaseSHA: "base", StartSHA: "start"}}
	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects/10/merge_requests/5/versions":    versionsHandler(versions),
		"/api/v4/projects/10/merge_requests/5/discussions": discussionHandler(true),
	})

	result, err := c.PostInlineComment(context.Background(), "10", 5, provider.InlineComment{
		FilePath: "src/main.go",
		Line:     10,
		Body:     "look here",
		NewLine:  true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "disc-1" {
		t.Errorf("expected ID=disc-1, got %s", result.ID)
	}
}

func TestPostInlineComment_OldLine(t *testing.T) {
	versions := []gitlabMRVersion{{ID: 1, HeadSHA: "head", BaseSHA: "base", StartSHA: "start"}}
	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects/10/merge_requests/6/versions":    versionsHandler(versions),
		"/api/v4/projects/10/merge_requests/6/discussions": discussionHandler(false),
	})

	result, err := c.PostInlineComment(context.Background(), "10", 6, provider.InlineComment{
		FilePath: "src/old.go",
		Line:     3,
		Body:     "old side",
		NewLine:  false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "disc-1" {
		t.Errorf("expected ID=disc-1, got %s", result.ID)
	}
}

func TestPostInlineComment_VersionsFetchFailure(t *testing.T) {
	_, c := newTestServer(t, map[string]http.HandlerFunc{
		"/api/v4/projects/10/merge_requests/7/versions": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
	})

	_, err := c.PostInlineComment(context.Background(), "10", 7, provider.InlineComment{
		FilePath: "file.go",
		Line:     1,
		Body:     "nope",
		NewLine:  true,
	})
	if err != provider.ErrNotFound {
		t.Errorf("expected ErrNotFound from versions fetch, got %v", err)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
