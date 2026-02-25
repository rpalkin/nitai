# AI Reviewer — Design Review: Deferred Items

Items identified during design review to address in future iterations.

## Phase 1 Implementation Review

### [BUG] Summary comment re-posted on every Restate retry
`postreview/service.go:71` — The summary comment is posted before loading unposted inline comments, but there's no idempotency guard on it. If the handler retries after posting the summary but before completing, duplicate summary comments appear on the MR. Inline comments have the `posted` flag; the summary doesn't. Fix: store a `summary_comment_id` on the review run row, skip re-posting if already set.

### [BUG] `MRNumber` type mismatch between api-server and go-services
`api-server/internal/restate/client.go:31` uses `MRNumber int64`, but `go-services/internal/prreview/service.go:29` uses `MRNumber int`. JSON serialization works in practice but this is a latent bug. Pick one and be consistent.

### [BUG] `review_runs.summary` column may be missing from migration chain
The init migration (`000001_init.up.sql`) creates `review_runs` without a `summary` column, but `UpdateReviewRunSummary` and `GetReviewRun` read/write it. Migrations 000002 and 000003 add `posted` and `deleted_at` respectively. Verify the column exists or add a migration.

### [BUG] E2e test has wrong RPC method names / response field paths
`tests/e2e.sh:161` calls `UpdateRepo` but the proto defines `EnableReview`/`DisableReview`. Line 140 extracts `.providerId` but the response wraps it in `provider.id`. Line 175 tries `.runId // .reviewRunId` but the response wraps it in `reviewRun.id`. The test will fail at runtime.

### [DX] `newProvider` and `classifyProviderError` duplicated across services
`difffetcher/service.go:95-105` and `postreview/service.go:102-112` are identical. The entire `provider/` and `crypto/` packages are also duplicated between `api-server` and `go-services`. Extract a shared Go module referenced via `replace` directives (same pattern as `gen/go`) before the duplication drifts.

### [DX] No `go.work` file for the multi-module setup
Three Go modules (`api-server`, `go-services`, `gen/go`) with no `go.work`. `gopls` (which `CLAUDE.md` requires) can't navigate across module boundaries without it. Developers need to open each module root separately.

### [DX] No `make dev` or local-run instructions
The only way to run anything is via Docker. A `make dev` target that starts only infra dependencies (postgres, restate) would speed up the inner dev loop for Go/Python development.

### [DX] `contains()` helper reimplements `strings.Contains()`
`go-services/internal/provider/gitlab/gitlab_test.go:415-426` — Custom `contains` and `containsStr` functions do exactly what `strings.Contains` does.

### [DX] Unit test coverage is thin
Tests exist for the GitLab client and crypto. No tests for: PRReview orchestrator, PostReview comment-posting loop with idempotency, handler layer (provider creation with transaction, review triggering), Python reviewer service. The orchestrator and posting logic are the most critical paths.

### [DX] `InsertReviewComments` does N individual INSERTs
`go-services/internal/db/queries.go:98-109` — Each comment is a separate `pool.Exec`. For 20+ comments this is 20 round trips. Use `pgx.Batch` or a single multi-row INSERT.

### [OPS] No structured logging
Both Go services use `log.Printf` / `log.Fatal` (stdlib plain text). The spec calls for `slog` structured JSON with `org_id`, `repo_id`, `pr_number` correlation. Plain text logs are very hard to search in production.

### [OPS] No `/healthz` endpoint on api-server
The e2e test polls `$API_BASE/healthz` but no such handler is registered. Works by accident because ConnectRPC returns a non-error for unknown paths. Add a real health check.

### [OPS] No resource limits in docker-compose.yml
No `mem_limit`, `cpus`, or `deploy.resources` on any service. The Python reviewer can consume significant memory for large diffs. One runaway container can OOM-kill everything on a single-server deployment.

### [OPS] PostgreSQL credentials hardcoded in docker-compose.yml
`POSTGRES_USER: ai_reviewer`, `POSTGRES_PASSWORD: ai_reviewer` are hardcoded values. Should reference env vars (`${POSTGRES_PASSWORD}`) so operators don't deploy with default credentials.

### [OPS] No log rotation or max-size on containers
Docker's default json-file log driver has no size limit. The reviewer service can produce verbose LLM interaction logs that fill the disk over time.

### [OPS] No graceful shutdown drain on the worker
`go-services/cmd/worker/main.go` — Context cancels on SIGINT/SIGTERM but there's no drain period. In-flight requests may be killed mid-execution during deploys, worsened by the summary re-posting bug above.

### [MINOR] No `.dockerignore`
Docker build context for go-services is `.` (repo root), sending the entire repo including `db/`, `pgdata/`, `restate-data/`. Add `.dockerignore` to exclude data directories.

### [MINOR] Reviewer Dockerfile doesn't pin dependencies
`RUN pip install --no-cache-dir .` with no lock file. Builds may not be reproducible.

### [MINOR] No index on `review_runs.status`
Querying "all running reviews" (for monitoring/dashboards) will table-scan.

### [MINOR] api-server imports gitlab client directly instead of through interface
`handler/provider.go:98` calls `gitlab.New()` directly. The `api-server/internal/provider/` package duplicates the full interface but only `ListRepos` is used.

## Review Pipeline

### Diff-hash dedup can miss meaningful re-review triggers
The pipeline skips review when `diff_hash` is unchanged, but the *context* can change: a dependency was updated on the target branch, a new org-level instruction was added, or the Qdrant index was re-embedded with a better model. The reviewer would now produce different output, but the skip logic prevents it. Consider adding a `review_config_version` or hash of the instruction set alongside the diff hash, so instruction changes also trigger re-reviews.

### No partial review for large diffs
PRs over 5,000 lines get a "too large" comment and nothing else. Many large PRs have a few critical files buried in boilerplate. Consider reviewing only non-generated/non-vendor files (after ignore rules are applied), or reviewing the most important subset and noting that the rest was skipped.

### Reviewer gets the full filtered diff in a single prompt
For a 4,000-line diff (just under the limit), stuffing everything into one LLM call risks hitting context limits and degrading review quality. The design doesn't mention a chunking or multi-pass strategy for large-but-below-threshold diffs. Consider splitting by file or logical group and aggregating results.

### Target branch HEAD vs. merge base for file reads
The file reader pins to `target branch HEAD at review time`, but the diff is computed against the merge base. If the target branch advanced since the PR was opened, the reviewer might read file content that doesn't match the diff context. Using the merge-base commit for file reads (or the PR head commit for files in the diff) would be more consistent.

## Re-review

### LLM inconsistency with comment updates
When the LLM decides whether to update or re-post old comments, it can be inconsistent — e.g., if an issue moved from line 42 to line 47, it might sometimes update the old comment, sometimes post a duplicate. Mitigate by giving the LLM old comments with their IDs and explicitly asking it to reference the ID when updating. Acceptable noise for v1.

## Security

### 1. `.review-rules.yaml` from PR head is exploitable
Reading rules from the PR head ref means an attacker can submit a PR that modifies `.review-rules.yaml` to add `ignore: ["**"]` and effectively disable review for their own malicious code changes. The "rules_modified" warning comment is easy to miss. Consider: always use the target branch version for ignore rules, or require a separate approval for rule changes.

### 2. Webhook secret shown once, no rotation
The webhook secret is "displayed to the admin" on creation with no mention of rotation. If a secret is compromised, there's no way to regenerate it without re-creating the provider. Add a `RotateWebhookSecret` endpoint.

### 3. No rate limiting on webhook endpoints
An attacker who discovers the webhook URL (predictable pattern `/webhooks/<provider-id>`) could flood it with fake events. Even with signature validation, the validation itself has a cost. Add rate limiting per provider.

### 4. `ENCRYPTION_KEY` as a single env var
AES-256-GCM with a single key from an env var means no key rotation, and if the key leaks, all provider tokens are compromised at once. Consider an envelope encryption scheme or integration with a KMS.

## Architectural

### Cancel-then-submit race in webhook handler
The webhook handler does `cancel → submit` as two separate Restate HTTP calls. If two pushes arrive nearly simultaneously, you can get: push1-cancel, push2-cancel (cancels push1's submit), push2-submit, push1-submit — resulting in the older push being reviewed. This needs to be atomic or use a compare-and-swap on the push SHA. The Virtual Object exclusivity prevents *concurrent execution* but not *stale invocations sitting in the queue*.

### Reconciliation every 5 minutes is aggressive for provider API limits
Each reconciliation run calls `ListOpenPRs` for *every enabled repo*. With 100 repos, that's 100+ API calls every 5 minutes. GitHub's rate limit is 5,000/hour — this alone would consume ~1,200/hour. Consider adaptive intervals, batching, or conditional checks (e.g., only reconcile repos that have had recent activity).

### 5. Bare clone concurrency claim is optimistic
The design states "concurrent `git fetch` and `git show` on bare repos are safe, so no file-level locking is needed." This is mostly true, but `git fetch` can GC/prune objects that a concurrent `git show` is referencing. If the reviewer reads a file at a specific SHA while a fetch triggers auto-GC, you could get transient errors. Consider disabling `gc.auto` on these bare repos.

### 6. 3-minute debounce is one-size-fits-all
The fixed 3-minute debounce may be too long for small repos (slow feedback) and too short for developers who push incrementally over 5+ minutes. Consider making it configurable per-repo or per-org.

### 7. No draft PR handling on push events
What happens when a push event arrives for an already-open draft PR? The webhook handler should check if the PR is a draft before dispatching `PRReviewWorkflow`.

### 8. Collection cloning for non-primary branches doesn't scale
The branch collection optimization clones the primary branch's Qdrant collection for non-primary target branches. If a repo has many active target branches (e.g., release branches), stale collections accumulate. No cleanup strategy is mentioned.

### 9. Missing retry/circuit-breaker on provider API calls
Retry logic is specified for OpenRouter but not for provider API calls (GitHub/GitLab). These APIs also have rate limits and can be flaky. The Go activities should have similar backoff and rate-limit-header awareness.

### No multi-tenancy isolation at the Qdrant level
Collections are named by `<repo-id>-<branch>`. There's no org-scoped namespace or access control on the Qdrant instance — any service can query any org's data. Acceptable for self-hosted single-tenant deployments, but becomes a data leak vector if the product ever moves to a shared deployment model.

### Local disk for repo clones has no capacity management
Repos accumulate on local disk with no eviction strategy. A customer with 200 repos, each multi-GB, will eventually fill the disk. Consider LRU eviction for repos that haven't been reviewed recently, or at minimum a disk usage alert.

### Search-MCP as a subprocess per review is expensive — *phase2 planned* (persistent HTTP container)
The Reviewer spawns Search-MCP as a stdio subprocess for every review. Each subprocess initializes a Qdrant client connection. For high-throughput installations, this is wasteful. Consider running Search-MCP as a persistent sidecar or using direct Qdrant client calls from the Reviewer.

## Missing Functionality

### 10. No comment resolution/outdated handling
When code is pushed and a previous inline comment's line no longer exists in the diff, the design says comments are "left as-is." But providers like GitHub mark these as "outdated." The reviewer should be aware of which previous comments are outdated vs. still on valid lines.

### 12. No feedback loop into the prompt
Feedback is collected and shown on a dashboard, but there's no mechanism to actually use it. Even a simple approach — like automatically deprioritizing instruction sets with consistently negative feedback — would close the loop.

### 13. No cost tracking or budget limits
LLM token usage is tracked as a metric but there's no budget enforcement. A single large org could run up massive costs. Consider per-org token budgets with configurable limits and alerts.

### 14. No webhook delivery verification
The reconciliation workflow is a good safety net, but consider also verifying webhook connectivity at provider setup time (e.g., sending a test ping and confirming receipt).

### No guardrails on LLM output quality
The reviewer returns structured output, but there's no validation beyond the Pydantic schema. The LLM could produce comments referencing files not in the diff, line numbers outside the changed range, or duplicates of its own output. A post-processing validation step would catch these before posting.

### No structured prompt versioning
When the review prompt template changes (new instructions format, different output schema), in-flight reviews might use the old prompt while new ones use the new one. There's no mechanism to track which prompt version produced which review, making it hard to A/B test or debug regressions. Store a `prompt_version` on each `PRReview` record.

## Provider Abstraction

### `GetPRDiff` format differences between providers are unaddressed
GitHub returns unified diffs via the API, but GitLab returns a different structure (list of diff objects per file). The spec assumes a common `PRDiff` type but doesn't define it. The mapping from GitLab's diff format to a unified diff that the LLM can consume needs to be specified, as subtle format differences will produce inconsistent review quality across providers.

### No pagination contract in `ListRepos` / `ListOpenPRs`
The interface mentions "each provider implementation handles pagination" but reconciliation calls `ListOpenPRs` expecting a complete list. If the implementation silently truncates at one page, PRs will be missed. The interface contract should explicitly guarantee exhaustive listing.

## Data Model

### 16. No PR state tracking beyond diff_hash
`PRReview` only tracks `diff_hash` for dedup. If you want to handle scenarios like "PR was rebased but content is identical," you'd also want to track the head SHA separately from the diff hash.

### 17. `ReviewComment.posted` boolean is fragile
A boolean `posted` flag doesn't capture partial failure well. If 8 of 10 comments post and the activity retries, you rely on `provider_comment_id` being set — but the comment could be posted to the provider while the DB write fails. Consider making the post + DB-update atomic or using an outbox pattern.

## Operational

### 19. No data retention policy
Review comments, activity logs, and indexed data will grow indefinitely. Specify TTL/archival policies, especially for `ActivityLog` and old `PRReview` records.

### 20. Python workers are single-threaded by default
Temporal Python workers use asyncio but don't parallelize CPU-bound work well. Embedding computation in the indexer is CPU/IO intensive. Consider specifying worker concurrency settings or running multiple worker replicas.

### Single Restate instance is a SPOF
Restate uses embedded RocksDB — there's no replication. If the Restate server dies and its disk is lost, all in-flight invocations, schedules (background indexing, reconciliation), and durable state are gone. Specify a backup strategy for Restate's data directory or plan for Restate Cloud.

## Minor

- **`MODEL_DIMENSIONS` duplication** — extract to a shared config or environment variable.
- **`GitProvider.DeleteComment`** exists in the interface but the design says old comments are "left as-is" — clarify if there's an unmentioned cleanup flow or if it's dead code.
