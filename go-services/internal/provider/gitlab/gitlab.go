package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"ai-reviewer/go-services/internal/provider"
)

// Client is a GitLab REST API v4 client.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client (useful for testing).
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) {
		cl.httpClient = c
	}
}

// New creates a GitLab client. baseURL should be the GitLab instance root
// (e.g. "https://gitlab.com"), without a trailing slash.
func New(baseURL, token string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: http.DefaultClient,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func (c *Client) newRequest(ctx context.Context, method, rawURL string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	return c.httpClient.Do(req)
}

func checkStatus(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusUnauthorized:
		return provider.ErrUnauthorized
	case http.StatusForbidden:
		return provider.ErrForbidden
	case http.StatusNotFound:
		return provider.ErrNotFound
	case http.StatusBadRequest:
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%w: %s", provider.ErrInvalidInput, strings.TrimSpace(string(body)))
	case http.StatusTooManyRequests:
		return provider.ErrRateLimited
	default:
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gitlab: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

func decodeJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// ── ListRepos ─────────────────────────────────────────────────────────────────

// ListRepos returns all repositories the authenticated user is a member of,
// following X-Next-Page pagination.
func (c *Client) ListRepos(ctx context.Context) ([]provider.Repo, error) {
	var repos []provider.Repo
	nextPage := "1"

	for nextPage != "" {
		u := fmt.Sprintf("%s/api/v4/projects?membership=true&per_page=100&page=%s", c.baseURL, url.QueryEscape(nextPage))
		req, err := c.newRequest(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.do(req)
		if err != nil {
			return nil, err
		}
		if err := checkStatus(resp); err != nil {
			resp.Body.Close()
			return nil, err
		}

		var projects []gitlabProject
		if err := decodeJSON(resp, &projects); err != nil {
			return nil, fmt.Errorf("gitlab: decode projects: %w", err)
		}

		for _, p := range projects {
			repos = append(repos, provider.Repo{
				RemoteID: strconv.Itoa(p.ID),
				Name:     p.Name,
				FullPath: p.PathWithNamespace,
				HTTPURL:  p.HTTPURLToRepo,
			})
		}

		nextPage = resp.Header.Get("X-Next-Page")
	}

	return repos, nil
}

// ── GetMRDetails ──────────────────────────────────────────────────────────────

// GetMRDetails returns metadata for the given merge request.
func (c *Client) GetMRDetails(ctx context.Context, repoRemoteID string, mrNumber int) (*provider.MRDetails, error) {
	u := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d",
		c.baseURL, url.PathEscape(repoRemoteID), mrNumber)
	req, err := c.newRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}

	var mr gitlabMR
	if err := decodeJSON(resp, &mr); err != nil {
		return nil, fmt.Errorf("gitlab: decode MR: %w", err)
	}

	return &provider.MRDetails{
		Title:        mr.Title,
		Description:  mr.Description,
		Author:       mr.Author.Username,
		SourceBranch: mr.SourceBranch,
		TargetBranch: mr.TargetBranch,
		HeadSHA:      mr.SHA,
		Draft:        mr.Draft,
	}, nil
}

// ── GetMRDiff ────────────────────────────────────────────────────────────────

// GetMRDiff returns the unified diff for the given merge request.
// GitLab returns diff fragments without `diff --git` headers; this method
// reconstructs them so the output matches the standard unified diff format.
func (c *Client) GetMRDiff(ctx context.Context, repoRemoteID string, mrNumber int) (*provider.MRDiff, error) {
	u := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/changes",
		c.baseURL, url.PathEscape(repoRemoteID), mrNumber)
	req, err := c.newRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}

	var changes gitlabMRChanges
	if err := decodeJSON(resp, &changes); err != nil {
		return nil, fmt.Errorf("gitlab: decode MR changes: %w", err)
	}

	var (
		sb           strings.Builder
		changedFiles []provider.ChangedFile
		totalLines   int
	)

	for _, ch := range changes.Changes {
		oldPath := ch.OldPath
		newPath := ch.NewPath
		if ch.NewFile {
			oldPath = "/dev/null"
		}
		if ch.DeletedFile {
			newPath = "/dev/null"
		}

		// Reconstruct unified diff header.
		fmt.Fprintf(&sb, "diff --git a/%s b/%s\n", ch.OldPath, ch.NewPath)
		if ch.NewFile {
			fmt.Fprintf(&sb, "new file mode 100644\n")
		} else if ch.DeletedFile {
			fmt.Fprintf(&sb, "deleted file mode 100644\n")
		}
		fmt.Fprintf(&sb, "--- %s\n", aPath(oldPath))
		fmt.Fprintf(&sb, "+++ %s\n", bPath(newPath))
		sb.WriteString(ch.Diff)
		if len(ch.Diff) > 0 && ch.Diff[len(ch.Diff)-1] != '\n' {
			sb.WriteByte('\n')
		}

		totalLines += countChangedLines(ch.Diff)

		changedFiles = append(changedFiles, provider.ChangedFile{
			OldPath: ch.OldPath,
			NewPath: ch.NewPath,
			Diff:    ch.Diff,
			NewFile: ch.NewFile,
			Deleted: ch.DeletedFile,
			Renamed: ch.RenamedFile,
		})
	}

	return &provider.MRDiff{
		UnifiedDiff:  sb.String(),
		ChangedFiles: changedFiles,
		ChangedLines: totalLines,
	}, nil
}

// aPath formats the --- path line for unified diff output.
func aPath(p string) string {
	if p == "/dev/null" {
		return p
	}
	return "a/" + p
}

// bPath formats the +++ path line for unified diff output.
func bPath(p string) string {
	if p == "/dev/null" {
		return p
	}
	return "b/" + p
}

// countChangedLines counts lines starting with '+' or '-' (excluding the @@
// hunk headers and the +++ / --- file header lines).
func countChangedLines(diff string) int {
	n := 0
	for _, line := range strings.Split(diff, "\n") {
		if len(line) == 0 {
			continue
		}
		ch := line[0]
		if (ch == '+' || ch == '-') && !strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
			n++
		}
	}
	return n
}

// ── PostComment ───────────────────────────────────────────────────────────────

// PostComment posts a top-level MR note (non-inline comment).
func (c *Client) PostComment(ctx context.Context, repoRemoteID string, mrNumber int, body string) (*provider.CommentResult, error) {
	u := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/notes",
		c.baseURL, url.PathEscape(repoRemoteID), mrNumber)

	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return nil, err
	}

	req, err := c.newRequest(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}

	var note gitlabNote
	if err := decodeJSON(resp, &note); err != nil {
		return nil, fmt.Errorf("gitlab: decode note: %w", err)
	}

	return &provider.CommentResult{ID: strconv.Itoa(note.ID)}, nil
}

// ── PostInlineComment ─────────────────────────────────────────────────────────

// PostInlineComment posts a diff comment anchored to a specific line.
// It fetches the latest MR version to obtain the required SHA values.
func (c *Client) PostInlineComment(ctx context.Context, repoRemoteID string, mrNumber int, comment provider.InlineComment) (*provider.CommentResult, error) {
	version, err := c.getMRVersions(ctx, repoRemoteID, mrNumber)
	if err != nil {
		return nil, err
	}

	position := map[string]any{
		"base_sha":      version.BaseSHA,
		"head_sha":      version.HeadSHA,
		"start_sha":     version.StartSHA,
		"position_type": "text",
		"new_path":      comment.FilePath,
		"old_path":      comment.FilePath,
	}
	if comment.NewLine {
		position["new_line"] = comment.Line
	} else {
		position["old_line"] = comment.Line
	}

	payload, err := json.Marshal(map[string]any{
		"body":     comment.Body,
		"position": position,
	})
	if err != nil {
		return nil, err
	}

	u := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/discussions",
		c.baseURL, url.PathEscape(repoRemoteID), mrNumber)
	req, err := c.newRequest(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}

	var disc gitlabDiscussion
	if err := decodeJSON(resp, &disc); err != nil {
		return nil, fmt.Errorf("gitlab: decode discussion: %w", err)
	}

	return &provider.CommentResult{ID: disc.ID}, nil
}

// getMRVersions returns the latest version for a merge request, which contains
// the base/head/start SHAs required by the discussion position payload.
func (c *Client) getMRVersions(ctx context.Context, repoRemoteID string, mrNumber int) (*gitlabMRVersion, error) {
	u := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/versions",
		c.baseURL, url.PathEscape(repoRemoteID), mrNumber)
	req, err := c.newRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}

	var versions []gitlabMRVersion
	if err := decodeJSON(resp, &versions); err != nil {
		return nil, fmt.Errorf("gitlab: decode versions: %w", err)
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("gitlab: no versions found for MR %d", mrNumber)
	}

	return &versions[0], nil
}
