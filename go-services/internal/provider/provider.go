package provider

import (
	"context"
	"errors"
)

// Sentinel errors returned by GitProvider implementations.
var (
	ErrNotFound     = errors.New("not found")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrRateLimited  = errors.New("rate limited")
	ErrInvalidInput = errors.New("invalid input") // e.g. invalid inline comment position
)

// GitProvider abstracts VCS platform operations needed by the reviewer.
// repoRemoteID is provider-specific (e.g. numeric string for GitLab, "owner/repo" for GitHub).
// mrNumber is the MR/PR number (GitLab MR IID).
// No retries are performed here — callers (Restate services) handle retry logic.
type GitProvider interface {
	ListRepos(ctx context.Context) ([]Repo, error)
	GetMRDiff(ctx context.Context, repoRemoteID string, mrNumber int) (*MRDiff, error)
	GetMRDetails(ctx context.Context, repoRemoteID string, mrNumber int) (*MRDetails, error)
	PostComment(ctx context.Context, repoRemoteID string, mrNumber int, body string) (*CommentResult, error)
	PostInlineComment(ctx context.Context, repoRemoteID string, mrNumber int, comment InlineComment) (*CommentResult, error)
}

// Repo is a repository accessible to the authenticated user.
type Repo struct {
	RemoteID string // provider-specific identifier
	Name     string
	FullPath string
	HTTPURL  string
}

// MRDiff holds the diff for a merge request.
type MRDiff struct {
	UnifiedDiff  string
	ChangedFiles []ChangedFile
	ChangedLines int
}

// ChangedFile is a single file changed in a merge request.
type ChangedFile struct {
	OldPath string
	NewPath string
	Diff    string
	NewFile bool
	Deleted bool
	Renamed bool
}

// MRDetails holds metadata about a merge request.
type MRDetails struct {
	Title        string
	Description  string
	Author       string
	SourceBranch string
	TargetBranch string
	HeadSHA      string
	Draft        bool
}

// InlineComment is a comment anchored to a specific line in a file.
type InlineComment struct {
	FilePath string
	Line     int
	Body     string
	NewLine  bool // true → comment on new (right) side; false → old (left) side
}

// CommentResult is the result of posting a comment.
type CommentResult struct {
	ID string
}
