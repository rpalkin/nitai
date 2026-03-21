//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	apiv1 "ai-reviewer/gen/api/v1"
	"connectrpc.com/connect"
)

func TestActivityLogLifecycle(t *testing.T) {
	t.Parallel()
	tc := NewTestContext(t)

	resp, err := clients.Activity.ListActivityLogs(context.Background(),
		connect.NewRequest(&apiv1.ListActivityLogsRequest{}))
	if err != nil {
		t.Fatalf("ListActivityLogs: %v", err)
	}
	initialCount := len(resp.Msg.Logs)
	t.Logf("Initial activity log count: %d", initialCount)

	providerID := tc.ProviderID
	repoID := tc.RepoID

	t.Logf("Provider created: %s, repo enabled: %s", providerID, repoID)

	triggerResp, err := clients.Review.TriggerReview(context.Background(),
		connect.NewRequest(&apiv1.TriggerReviewRequest{
			RepoId:   repoID,
			MrNumber: 1,
		}))
	if err != nil {
		t.Fatalf("TriggerReview: %v", err)
	}
	runID := triggerResp.Msg.ReviewRun.Id
	t.Logf("Triggered review: runID=%s", runID)

	_, err = clients.Repo.DisableReview(context.Background(),
		connect.NewRequest(&apiv1.DisableReviewRequest{RepoId: repoID}))
	if err != nil {
		t.Fatalf("DisableReview: %v", err)
	}
	t.Log("Disabled review")

	_, err = clients.Provider.DeleteProvider(context.Background(),
		connect.NewRequest(&apiv1.DeleteProviderRequest{Id: providerID}))
	if err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	t.Log("Deleted provider")

	time.Sleep(1 * time.Second)

	listResp, err := clients.Activity.ListActivityLogs(context.Background(),
		connect.NewRequest(&apiv1.ListActivityLogsRequest{}))
	if err != nil {
		t.Fatalf("ListActivityLogs: %v", err)
	}

	logs := listResp.Msg.Logs
	t.Logf("Activity logs returned: %d", len(logs))

	expectedTypes := map[string]bool{
		"provider.created":     false,
		"repo.review_enabled":  false,
		"review.triggered":     false,
		"repo.review_disabled": false,
		"provider.deleted":     false,
	}
	for _, log := range logs {
		if _, ok := expectedTypes[log.EventType]; ok {
			expectedTypes[log.EventType] = true
		}
	}

	for eventType, found := range expectedTypes {
		if !found {
			t.Errorf("missing expected event type: %s", eventType)
		}
	}

	providerLogs, err := clients.Activity.ListActivityLogs(context.Background(),
		connect.NewRequest(&apiv1.ListActivityLogsRequest{
			EventType: "provider.created",
		}))
	if err != nil {
		t.Fatalf("ListActivityLogs with event_type filter: %v", err)
	}
	for _, log := range providerLogs.Msg.Logs {
		if log.EventType != "provider.created" {
			t.Errorf("expected only provider.created events, got: %s", log.EventType)
		}
	}

	repoLogs, err := clients.Activity.ListActivityLogs(context.Background(),
		connect.NewRequest(&apiv1.ListActivityLogsRequest{
			RepoId: repoID,
		}))
	if err != nil {
		t.Fatalf("ListActivityLogs with repo_id filter: %v", err)
	}
	for _, log := range repoLogs.Msg.Logs {
		if log.RepoId != repoID {
			t.Errorf("expected only events for repo %s, got repo_id: %s", repoID, log.RepoId)
		}
	}

	page1, err := clients.Activity.ListActivityLogs(context.Background(),
		connect.NewRequest(&apiv1.ListActivityLogsRequest{
			Limit:  2,
			Offset: 0,
		}))
	if err != nil {
		t.Fatalf("ListActivityLogs pagination page 1: %v", err)
	}
	if len(page1.Msg.Logs) > 2 {
		t.Errorf("expected at most 2 logs per page, got %d", len(page1.Msg.Logs))
	}
	if page1.Msg.TotalCount < int32(len(logs)) {
		t.Errorf("total_count %d less than actual log count %d", page1.Msg.TotalCount, len(logs))
	}

	if len(logs) > 2 {
		page2, err := clients.Activity.ListActivityLogs(context.Background(),
			connect.NewRequest(&apiv1.ListActivityLogsRequest{
				Limit:  2,
				Offset: 2,
			}))
		if err != nil {
			t.Fatalf("ListActivityLogs pagination page 2: %v", err)
		}
		if len(page2.Msg.Logs) == 0 {
			t.Error("expected at least one log on page 2")
		}
	}

	t.Log("Activity log lifecycle test passed")
}
