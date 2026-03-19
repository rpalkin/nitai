# Providers

The provider abstraction unifies access to GitHub and GitLab (cloud and self-hosted) through a common interface.

## Architecture

All three providers (GitHub, GitLab Cloud, GitLab self-hosted) are accessed through a common Go interface:

```go
type GitProvider interface {
    ListRepos(ctx context.Context) ([]Repo, error)
    ListOpenPRs(ctx context.Context, repoID string) ([]PR, error)
    GetPRDiff(ctx context.Context, prID string) (*PRDiff, error)
    GetPRDetails(ctx context.Context, prID string) (*PRDetails, error)
    GetPRComments(ctx context.Context, prID string) ([]Comment, error)
    PostComment(ctx context.Context, prID string, comment Comment) error
    PostInlineComment(ctx context.Context, prID string, comment InlineComment) error
    UpdateComment(ctx context.Context, commentID string, comment Comment) error
    DeleteComment(ctx context.Context, commentID string) error
    GetFileContent(ctx context.Context, ref string, path string) ([]byte, error)
}
```

Each provider implementation handles auth, API pagination, and mapping to/from the common types.

## Webhook Handling

- Each provider instance gets a unique webhook URL: `/webhooks/<provider-id>`
- Webhook secret is generated on provider creation and displayed to the admin for manual configuration on GitHub/GitLab
- API Server validates webhook signatures per provider type
- Supported events:
  - **PR open/update/synchronize** → cancels any in-flight `PRReview` invocation for the same PR, then submits a new one; draft PRs create a `PRReview` record with `status=draft` instead (no review dispatched)
  - **PR marked ready for review** → clears the `draft` status, cancels + submits `PRReview` invocation
  - **Push to primary branch** → submits `IndexMainBranch` invocation
  - All other events are ignored
- The webhook handler calls Restate's HTTP ingress directly (cancel + submit). No in-process queuing.
- Concurrency is managed by Virtual Object key exclusivity

## PR Reconciliation

Periodic reconciliation catches missed webhooks (crashes, misconfiguration, network issues) without requiring an inbox table or delivery guarantees on the webhook path.

**Virtual Object:** `PRReconciliation`
**Key:** `<repo_id>`
**Handler:** `Run` (exclusive)
**Schedule:** Self-scheduled, configurable interval (default: every 5 minutes).

```
+---------------------------------------------+
| List open PRs via provider API               |
|  - for each PR, fetch head commit SHA       |
+----------------------+----------------------+
                        |
                        v
+---------------------------------------------+
| Reconcile                                    |
|  - compare open PRs against PRReview records|
|  - skip PRs opened before the repo was enabled 
|    (repo.review_enabled_at timestamp)       |
|  - for draft PRs ~> upsert PRReview with    |
|    status=draft (no review dispatched)      |
|  - for each unreviewed or stale PR (head SHA ≠ |
|    last reviewed diff's head SHA):           |
|    ~> cancel + submit PRReview invocation   |
+----------------------+----------------------+
                        |
                        v
+---------------------------------------------+
| Schedule next run                            |
|  - ctx.send(self, delay=5m)                 |
|  - durable: survives restarts               |
+---------------------------------------------+
```

**Key behaviors:**
- **Pre-existing PRs are skipped:** When review is enabled for a repo, a one-time snapshot creates `PRReview` records with `status=skipped` for all currently open PRs. The reconciliation loop then naturally ignores these PRs (they already have a review record). Admins can trigger reviews for skipped PRs explicitly via the Admin Console.
- **Idempotent:** If a webhook already triggered the review, `CheckPreviousReview` in the PR review pipeline detects the matching diff hash and skips— no duplicate work.
- **Single instance guaranteed:** The Virtual Object's exclusive handler ensures only one reconciliation runs per repo at a time.

## Key Decisions

- **Common `GitProvider` interface** — abstracts GitHub and GitLab API differences
- **Unique webhook URL per provider: `/webhooks/<provider-id>`** — easy routing, per-provider secrets
- **Signature validation per provider type** — ensures webhook authenticity
- **Cancel-then-submit via Restate ingress on webhook** — atomic handling of rapid pushes