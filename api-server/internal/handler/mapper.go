package handler

import (
	"time"

	apiv1 "ai-reviewer/gen/api/v1"
	"ai-reviewer/api-server/internal/db"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func providerTypeToString(t apiv1.ProviderType) string {
	switch t {
	case apiv1.ProviderType_PROVIDER_TYPE_GITLAB_SELF_HOSTED:
		return "gitlab_self_hosted"
	case apiv1.ProviderType_PROVIDER_TYPE_GITLAB_CLOUD:
		return "gitlab_cloud"
	case apiv1.ProviderType_PROVIDER_TYPE_GITHUB:
		return "github"
	default:
		return ""
	}
}

func stringToProviderType(s string) apiv1.ProviderType {
	switch s {
	case "gitlab_self_hosted":
		return apiv1.ProviderType_PROVIDER_TYPE_GITLAB_SELF_HOSTED
	case "gitlab_cloud":
		return apiv1.ProviderType_PROVIDER_TYPE_GITLAB_CLOUD
	case "github":
		return apiv1.ProviderType_PROVIDER_TYPE_GITHUB
	default:
		return apiv1.ProviderType_PROVIDER_TYPE_UNSPECIFIED
	}
}

func stringToReviewStatus(s string) apiv1.ReviewStatus {
	switch s {
	case "pending":
		return apiv1.ReviewStatus_REVIEW_STATUS_PENDING
	case "running":
		return apiv1.ReviewStatus_REVIEW_STATUS_RUNNING
	case "completed":
		return apiv1.ReviewStatus_REVIEW_STATUS_COMPLETED
	case "failed":
		return apiv1.ReviewStatus_REVIEW_STATUS_FAILED
	default:
		return apiv1.ReviewStatus_REVIEW_STATUS_UNSPECIFIED
	}
}

func toTimestamp(t time.Time) *timestamppb.Timestamp {
	return timestamppb.New(t)
}

func providerRowToProto(p db.ProviderRow) *apiv1.Provider {
	return &apiv1.Provider{
		Id:        p.ID,
		Type:      stringToProviderType(p.Type),
		Name:      p.Name,
		BaseUrl:   p.BaseURL,
		CreatedAt: toTimestamp(p.CreatedAt),
	}
}

func repoRowToProto(r db.RepoRow) *apiv1.Repository {
	return &apiv1.Repository{
		Id:            r.ID,
		ProviderId:    r.ProviderID,
		RemoteId:      r.RemoteID,
		Name:          r.Name,
		FullPath:      r.FullPath,
		ReviewEnabled: r.ReviewEnabled,
		CreatedAt:     toTimestamp(r.CreatedAt),
	}
}

func reviewRunToProto(run db.ReviewRunRow, comments []db.ReviewCommentRow) *apiv1.ReviewRun {
	protoComments := make([]*apiv1.ReviewComment, len(comments))
	for i, c := range comments {
		protoComments[i] = &apiv1.ReviewComment{
			Id:          c.ID,
			ReviewRunId: c.ReviewRunID,
			FilePath:    c.FilePath,
			LineStart:   int32(c.LineStart),
			LineEnd:     int32(c.LineEnd),
			Body:        c.Body,
		}
	}
	return &apiv1.ReviewRun{
		Id:        run.ID,
		RepoId:    run.RepoID,
		MrNumber:  run.MRNumber,
		Status:    stringToReviewStatus(run.Status),
		Comments:  protoComments,
		CreatedAt: toTimestamp(run.CreatedAt),
		UpdatedAt: toTimestamp(run.UpdatedAt),
	}
}
