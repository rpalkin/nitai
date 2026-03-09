//go:build integration

package gitlab

import (
	"context"
	"os"
	"strconv"
	"testing"
)

// Integration tests require a real GitLab instance. Set the following env vars:
//
//	GITLAB_URL        — GitLab base URL (e.g. https://gitlab.com)
//	GITLAB_TOKEN      — Personal access token with api scope
//	GITLAB_PROJECT_ID — Numeric project ID to test against
//	GITLAB_MR_IID     — MR IID within that project
//
// Run: go test -tags=integration -v ./internal/provider/gitlab/
func integrationClient(t *testing.T) (*Client, string, int) {
	t.Helper()
	baseURL := os.Getenv("GITLAB_URL")
	token := os.Getenv("GITLAB_TOKEN")
	projectID := os.Getenv("GITLAB_PROJECT_ID")
	mrIIDStr := os.Getenv("GITLAB_MR_IID")

	if baseURL == "" || token == "" || projectID == "" || mrIIDStr == "" {
		t.Skip("GITLAB_URL, GITLAB_TOKEN, GITLAB_PROJECT_ID, GITLAB_MR_IID not set — skipping integration tests")
	}

	mrIID, err := strconv.Atoi(mrIIDStr)
	if err != nil {
		t.Fatalf("GITLAB_MR_IID must be an integer: %v", err)
	}

	return New(baseURL, token), projectID, mrIID
}

func TestIntegration_ListRepos(t *testing.T) {
	c, _, _ := integrationClient(t)

	repos, err := c.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	t.Logf("ListRepos returned %d repos", len(repos))
	if len(repos) == 0 {
		t.Error("expected at least one repo")
	}
}

func TestIntegration_GetMRDetails(t *testing.T) {
	c, projectID, mrIID := integrationClient(t)

	details, err := c.GetMRDetails(context.Background(), projectID, mrIID)
	if err != nil {
		t.Fatalf("GetMRDetails: %v", err)
	}
	t.Logf("MR title: %s, author: %s, head: %s", details.Title, details.Author, details.HeadSHA)
	if details.Title == "" {
		t.Error("expected non-empty title")
	}
}

func TestIntegration_GetMRDiff(t *testing.T) {
	c, projectID, mrIID := integrationClient(t)

	diff, err := c.GetMRDiff(context.Background(), projectID, mrIID)
	if err != nil {
		t.Fatalf("GetMRDiff: %v", err)
	}
	t.Logf("GetMRDiff: %d files, %d changed lines", len(diff.ChangedFiles), diff.ChangedLines)
}
