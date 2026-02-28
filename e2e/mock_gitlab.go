//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// PostedNote records a summary note POST.
type PostedNote struct {
	ProjectID string
	MRNumber  string
	Body      string
}

// PostedDiscussion records an inline comment POST.
type PostedDiscussion struct {
	ProjectID string
	MRNumber  string
	Body      string
	Position  DiscussionPosition
}

type DiscussionPosition struct {
	BaseSHA      string `json:"base_sha"`
	HeadSHA      string `json:"head_sha"`
	StartSHA     string `json:"start_sha"`
	PositionType string `json:"position_type"`
	NewPath      string `json:"new_path"`
	OldPath      string `json:"old_path"`
	NewLine      int    `json:"new_line,omitempty"`
	OldLine      int    `json:"old_line,omitempty"`
}

// RecordedRequest stores a received HTTP request for assertion.
type RecordedRequest struct {
	Method string
	Path   string
	Body   []byte
}

// GitLabMock is a configurable mock GitLab API server.
type GitLabMock struct {
	Server *httptest.Server

	mu                sync.Mutex
	requests          []RecordedRequest
	postedNotes       []PostedNote
	postedDiscussions []PostedDiscussion

	// Per-project config
	projects []GitLabProject

	// Per-MR config: "projectID/mrIID" -> config
	mrConfigs map[string]*MRConfig
}

type GitLabProject struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
}

type MRConfig struct {
	Details    json.RawMessage // GET /merge_requests/:iid response
	Changes    json.RawMessage // GET /merge_requests/:iid/changes response
	Versions   json.RawMessage // GET /merge_requests/:iid/versions response
	StatusCode int             // Override status code (0 = 200)
}

func NewGitLabMock() *GitLabMock {
	g := &GitLabMock{
		mrConfigs: make(map[string]*MRConfig),
	}
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		panic(err)
	}
	g.Server = httptest.NewUnstartedServer(http.HandlerFunc(g.handle))
	g.Server.Listener = l
	g.Server.Start()
	return g
}

// HostURL returns the mock server URL using host.docker.internal,
// so Docker containers on any platform can reach the host mock server.
func (g *GitLabMock) HostURL() string {
	port := portFromURL(g.Server.URL)
	return "http://host.docker.internal:" + port
}

func (g *GitLabMock) handle(w http.ResponseWriter, r *http.Request) {
	// Record the request
	var bodyBytes []byte
	if r.Body != nil {
		bodyBytes, _ = io.ReadAll(r.Body)
	}

	g.mu.Lock()
	g.requests = append(g.requests, RecordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Body:   bodyBytes,
	})
	g.mu.Unlock()

	segments := strings.Split(r.URL.Path, "/")
	// Path: /api/v4/projects/{id}/merge_requests/{iid}[/suffix]
	// segments: ["", "api", "v4", "projects", "{id}", "merge_requests", "{iid}", ...]

	w.Header().Set("Content-Type", "application/json")

	// GET /api/v4/projects (list projects)
	if r.Method == "GET" && len(segments) >= 4 && segments[3] == "projects" && len(segments) == 4 {
		page := r.URL.Query().Get("page")
		if page == "" || page == "1" {
			g.mu.Lock()
			projects := g.projects
			g.mu.Unlock()
			json.NewEncoder(w).Encode(projects)
		} else {
			w.Write([]byte("[]"))
		}
		return
	}

	// Routes under /api/v4/projects/{id}/merge_requests/{iid}
	if len(segments) >= 7 && segments[3] == "projects" && segments[5] == "merge_requests" {
		projectID := segments[4]
		mrIID := segments[6]
		suffix := ""
		if len(segments) > 7 {
			suffix = segments[7]
		}

		key := projectID + "/" + mrIID
		g.mu.Lock()
		cfg := g.mrConfigs[key]
		g.mu.Unlock()

		switch {
		case r.Method == "GET" && suffix == "":
			if cfg == nil {
				http.Error(w, `{"message":"404 Not found"}`, http.StatusNotFound)
				return
			}
			statusCode := cfg.StatusCode
			if statusCode == 0 {
				statusCode = http.StatusOK
			}
			w.WriteHeader(statusCode)
			w.Write(cfg.Details)

		case r.Method == "GET" && suffix == "changes":
			if cfg == nil {
				http.Error(w, `{"message":"404 Not found"}`, http.StatusNotFound)
				return
			}
			statusCode := cfg.StatusCode
			if statusCode == 0 {
				statusCode = http.StatusOK
			}
			w.WriteHeader(statusCode)
			w.Write(cfg.Changes)

		case r.Method == "GET" && suffix == "versions":
			if cfg == nil {
				http.Error(w, `{"message":"404 Not found"}`, http.StatusNotFound)
				return
			}
			statusCode := cfg.StatusCode
			if statusCode == 0 {
				statusCode = http.StatusOK
			}
			w.WriteHeader(statusCode)
			w.Write(cfg.Versions)

		case r.Method == "POST" && suffix == "notes":
			var payload struct {
				Body string `json:"body"`
			}
			json.Unmarshal(bodyBytes, &payload)
			g.mu.Lock()
			g.postedNotes = append(g.postedNotes, PostedNote{
				ProjectID: projectID,
				MRNumber:  mrIID,
				Body:      payload.Body,
			})
			g.mu.Unlock()
			w.Write([]byte(`{"id": 1001}`))

		case r.Method == "POST" && suffix == "discussions":
			var payload struct {
				Body     string             `json:"body"`
				Position DiscussionPosition `json:"position"`
			}
			json.Unmarshal(bodyBytes, &payload)
			g.mu.Lock()
			g.postedDiscussions = append(g.postedDiscussions, PostedDiscussion{
				ProjectID: projectID,
				MRNumber:  mrIID,
				Body:      payload.Body,
				Position:  payload.Position,
			})
			g.mu.Unlock()
			w.Write([]byte(`{"id": "disc-1"}`))

		default:
			http.Error(w, `{"message":"404 Not found"}`, http.StatusNotFound)
		}
		return
	}

	http.Error(w, `{"message":"404 Not found"}`, http.StatusNotFound)
}

func (g *GitLabMock) SetProjects(projects []GitLabProject) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.projects = projects
}

func (g *GitLabMock) SetMR(projectID, mrIID string, cfg *MRConfig) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.mrConfigs[projectID+"/"+mrIID] = cfg
}

func (g *GitLabMock) Requests() []RecordedRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]RecordedRequest, len(g.requests))
	copy(out, g.requests)
	return out
}

func (g *GitLabMock) RequestsTo(method, pathPrefix string) []RecordedRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []RecordedRequest
	for _, r := range g.requests {
		if r.Method == method && strings.HasPrefix(r.Path, pathPrefix) {
			out = append(out, r)
		}
	}
	return out
}

func (g *GitLabMock) Notes() []PostedNote {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]PostedNote, len(g.postedNotes))
	copy(out, g.postedNotes)
	return out
}

func (g *GitLabMock) Discussions() []PostedDiscussion {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]PostedDiscussion, len(g.postedDiscussions))
	copy(out, g.postedDiscussions)
	return out
}

func (g *GitLabMock) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = nil
	g.postedNotes = nil
	g.postedDiscussions = nil
}
