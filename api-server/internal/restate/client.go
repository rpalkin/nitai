package restate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Client sends fire-and-forget messages to the Restate ingress and cancels invocations via the admin API.
type Client struct {
	baseURL    string
	adminURL   string
	httpClient *http.Client
}

// New creates a new Restate client with both ingress and admin URLs.
func New(ingressURL, adminURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(ingressURL, "/"),
		adminURL:   strings.TrimRight(adminURL, "/"),
		httpClient: http.DefaultClient,
	}
}

// PRReviewRequest is the request body for the PRReview Run handler.
type PRReviewRequest struct {
	RunID    string `json:"run_id"`
	RepoID   string `json:"repo_id"`
	MRNumber int64  `json:"mr_number"`
	Force    bool   `json:"force"`
}

// sendResponse is the JSON body returned by Restate's /send endpoint.
type sendResponse struct {
	InvocationID string `json:"invocationId"`
	Status       string `json:"status"`
}

// SendPRReview sends a fire-and-forget PRReview/Run message to Restate and returns the invocation ID.
// key format: "{repo_id}-{mr_number}"
func (c *Client) SendPRReview(ctx context.Context, key string, req PRReviewRequest) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshaling request: %w", err)
	}

	url := fmt.Sprintf("%s/PRReview/%s/Run/send", c.baseURL, key)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("restate: unexpected status %d", resp.StatusCode)
	}

	var result sendResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}
	return result.InvocationID, nil
}

// CancelInvocation cancels a Restate invocation by ID. 404 (already completed) is silently ignored.
func (c *Client) CancelInvocation(ctx context.Context, invocationID string) error {
	url := fmt.Sprintf("%s/invocations/%s/cancel", c.adminURL, invocationID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, nil)
	if err != nil {
		return fmt.Errorf("creating cancel request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("cancel request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil // already completed, ignore
	}
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("restate cancel: unexpected status %d", resp.StatusCode)
	}
	return nil
}
