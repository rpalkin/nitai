# AI Reviewer — Technical Design Overview

## 1. System Purpose

AI Reviewer is a self-hosted automated PR review system that provides semantic code analysis. It posts summary comments and inline review comments on pull requests. It supports GitHub, GitLab Cloud, and self-hosted GitLab as git providers.

## 2. High-Level Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                        Admin Console (React/TS/Vite)                │
└────────────────────────────────┬────────────────────────────────────┘
                                 │ ConnectRPC
┌────────────────────────────────▼────────────────────────────────────┐
│                        API Server (Go / connect-go)                 │
│   ┌──────────────┐  ┌──────────────┐  ┌──────────────────────────┐ │
│   │  Admin API   │  │ Webhook API  │  │  Restate Ingress Client  │ │
│   └──────────────┘  └──────┬───────┘  └───────────┬──────────────┘ │
└─────────────┬──────────────┼──────────────────────┼────────────────┘
              │              │                      │
              ▼              │                      ▼
┌──────────────────┐         │         ┌─────────────────────────────┐
│   PostgreSQL     │         │         │     Restate Server           │
│                  │         │         │     (single binary, RocksDB) │
└──────────────────┘         │         └──────────┬──────────────────┘
                             │                    │
                   ┌─────────┴──────┬─────────────┼──────────┐
                   ▼                ▼             ▼          ▼
          ┌──────────────┐ ┌──────────────┐ ┌─────────┐ ┌──────────┐
          │ Go Service   │ │ Go Service   │ │Py Svc   │ │Py Svc    │
          │              │ │              │ │         │ │          │
          │- PRReview    │ │- ProviderSync│ │-Indexer │ │-Reviewer │
          │- DiffFetcher │ │- RepoSyncer  │ │         │ │-SearchMCP│
          │- PostReview  │ │              │ │         │ │          │
          └──────┬───────┘ └──────┬───────┘ └────┬────┘ └────┬─────┘
                 │                │              │           │
                 ▼                ▼              ▼           ▼
          Git Providers     Local Disk        Qdrant    OpenRouter
```

### Service Topology

Handlers are split across **language-specific Restate services** registered with the Restate Server:

| Service | Type | Language | Handlers |
|---|---|---|---|
| `PRReview` | Virtual Object | Go | Run (exclusive) |
| `ProviderSync` | Service | Go | Sync |
| `DiffFetcher` | Service | Go | FetchPRDetails, CheckPreviousReview, FetchRepoRules |
| `RepoSyncer` | Service | Go | SyncRepo |
| `PostReview` | Service | Go | Post |
| `Indexer` | Service | Python | IndexRepo |
| `Reviewer` | Service | Python | RunReview (Pydantic AI + Search-MCP + file reader) |

The **`PRReview` Virtual Object** is keyed by `<repo_id>-<pr_number>`. Its exclusive `Run` handler orchestrates the full review pipeline by making durable calls to the other services. Restate's cross-service invocation is language-transparent — the Go handler calls Python services over HTTP, managed by Restate.

## 3. Components

### 3.1 Admin Console (Frontend)
- **Tech:** React, TypeScript, Vite
- **Purpose:** Organization management, provider configuration, repo selection, custom review instructions, review feedback dashboard, activity logs, manual review triggers (live or dry-run)
- **Communication:** ConnectRPC to API Server

### 3.2 API Server (Go)
- **Tech:** Go, connect-go (ConnectRPC)
- **Responsibilities:**
  - Admin API — CRUD for organizations, providers, repos, instructions
  - Webhook endpoints — receive PR events from GitHub/GitLab, one URL per provider instance
  - Restate ingress client — cancels in-flight reviews and submits new invocations via Restate's HTTP ingress
  - Start Review API — trigger a live or dry-run review from the Admin Console. Bypasses the diff_hash check (force flag). Cancels any in-flight review for the same PR before starting.
- **Auth:** Email + password (JWT sessions). OAuth deferred to later.
- **No roles** — all users within an org have equal access.

### 3.3 PostgreSQL
- **Stores:** organizations, users, providers (credentials encrypted at rest via AES-256-GCM, key from `ENCRYPTION_KEY` env var), repos, review runs, inline comments (including body), activity logs, custom instructions with filter rules, review feedback
- **PR tracking:** stores diff hash per PR to detect re-reviews vs. new changes
- **Review comments:** full comment body stored alongside metadata (file, line, provider comment ID). This enables analytics, debugging, and resilience against comments deleted on the provider side.

### 3.4 Restate
- **Purpose:** Durable execution engine — orchestrates multi-step, retryable workflows with built-in state management
- **Deployment:** Single Rust binary with embedded RocksDB storage. No external database required.
- **Key services:**
  - **PRReview** (Virtual Object) — the main review pipeline, keyed by `<repo_id>-<pr_number>` (see section 5)
  - **ProviderSync** — triggered when a provider is added/updated; lists repos via provider API, upserts to DB
  - **IndexMainBranch** (Virtual Object) — periodic background indexing of primary branches, keyed by `<repo_id>` (see section 5.1)
- **Concurrency control:** `PRReview` is a Virtual Object with an exclusive `Run` handler. Only one review can execute per PR at a time. When a new push arrives, the webhook handler cancels the in-flight invocation via Restate's admin API (`PATCH /invocations/PRReview/<key>/Run/cancel`) and submits a fresh one. This prevents stale reviews from being posted.
- **UI:** Restate ships with a built-in web UI for inspecting running invocations, viewing execution timelines, debugging failures, and restarting failed invocations.

### 3.5 Diff Fetcher
- **Tech:** Go handler in `DiffFetcher` service
- **Purpose:** Fetches PR diff and metadata (title, description, changed files, commits) from provider APIs
- **Diff size limit:** If the diff exceeds **5,000 changed lines**, the handler returns a `diff_too_large` flag. The workflow skips the review and posts a single comment explaining the PR is too large for automated review.
- **Providers:** GitHub REST/GraphQL, GitLab REST — abstracted behind a common interface

### 3.6 Repo Syncer
- **Tech:** Go handler in `RepoSyncer` service
- **Purpose:** Maintains a bare clone of each enabled repo on local disk; pulls latest target branch on demand
- **Storage:** Local disk, path convention: `<data-dir>/<org-id>/<repo-id>/`. Object storage planned for future.
- **Concurrency:** The local clone is a bare git object store. Concurrent `git fetch` and `git show` operations on bare repos are safe, so no file-level locking is needed. `SyncRepo` only runs `git fetch`; all file reads go through `git show <sha>:<path>`.

### 3.7 Indexer (Python)
- **Tech:** Python Restate service (existing module in this repo, adapted as handler)
- **Purpose:** Walks the local repo clone, chunks code with tree-sitter, computes embeddings, upserts to Qdrant
- **Collection naming:** `<repo-id>-<branch>` — one collection per repo per target branch
- **Branch collection optimization:** When a PR targets a non-primary branch and no Qdrant collection exists for that branch yet, the indexer **clones the primary branch's collection** first, then re-indexes only the files that differ between the two branches. This avoids a full re-embedding for branches that share most of their code with main/master.

### 3.8 Qdrant
- **Purpose:** Vector database for semantic code search
- **Deployment:** Docker, data persisted to local disk

### 3.9 Search-MCP (Python)
- **Tech:** FastMCP server (existing module in this repo)
- **Purpose:** Exposes `list_collections` and `search` tools over MCP stdio, launched by the Reviewer handler as a subprocess

### 3.10 Reviewer (Python)
- **Tech:** Python Restate service, Pydantic AI
- **Purpose:** Runs LLM-based review of a PR
- **LLM:** OpenRouter (not user-configurable)
- **Inputs:** PR diff (filtered by ignore rules), previous review comments, custom instructions (from DB + repo-level `.review-rules.yaml`), repo metadata
- **Tools available to LLM:**
  - Search-MCP — semantic search across the indexed codebase
  - File reader — reads files at a pinned commit SHA via `git show <sha>:<path>` on the bare clone. The SHA comes from `FetchPRDetails` (target branch HEAD at review time). This eliminates race conditions with concurrent `SyncRepo` operations — the file reader always serves content consistent with the diff, regardless of subsequent fetches.
- **Output:** structured review (summary + list of inline comments with file, line, message, and optional update action referencing a previous comment ID)
- **Dry-run mode:** When triggered via the Admin Console, the reviewer returns its output without posting comments. This allows admins to preview and tune review quality before enabling live reviews.
- **OpenRouter resilience:** Retries use exponential backoff with jitter, respecting OpenRouter rate-limit headers (`Retry-After`, `X-RateLimit-Reset`). Restate's built-in retry policy handles transient failures. If the model is unavailable after retries, the invocation fails and can be inspected/restarted via the Restate UI.

## 4. Data Model (Key Entities)

| Entity | Key Fields |
|---|---|
| Organization | id, name, created_at |
| User | id, org_id, email, password_hash, created_at |
| Provider | id, org_id, type (github/gitlab/gitlab_self_hosted), api_url, token (encrypted), webhook_secret |
| Repository | id, org_id, provider_id, remote_id, name, full_name, default_branch, review_enabled |
| BranchIndex | id, repo_id, branch, last_indexed_commit, collection_name, updated_at |
| ReviewInstruction | id, org_id, content, filters (repos, file patterns, languages) |
| PRReview | id, repo_id, pr_number, diff_hash, status (pending/completed/failed/skipped/draft), summary, created_at, is_dry_run |
| ReviewComment | id, review_id, file_path, line, body, provider_comment_id, posted (bool) |
| ReviewFeedback | id, comment_id, user_identifier, rating (positive/negative), created_at |
| ActivityLog | id, org_id, repo_id, action, details, created_at |

## 5. PR Review Pipeline

**Virtual Object:** `PRReview`
**Key:** `<repo_id>-<pr_number>`
**Handler:** `Run` (exclusive — one execution per key at a time)

When a new push arrives for a PR that is already being reviewed, the webhook handler cancels the running invocation and submits a new one. This ensures only the latest push is reviewed.

```
Webhook received (API Server)
  │
  ├─ Cancel running invocation (if any):
  │    PATCH /invocations/PRReview/<key>/Run/cancel
  │
  ▼
Submit new invocation:
  POST /PRReview/<key>/Run
  │
  ▼
┌─────────────────────────────────────────────┐
│ Debounce (3 minutes)                         │
│  - ctx.sleep(3m) before proceeding           │
│  - if a new push arrives during the wait,    │
│    the webhook handler cancels this           │
│    invocation and starts a fresh one          │
│  - rapid pushes only pay the cost of one     │
│    review after the author stops pushing     │
└──────────────────────┬──────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────┐
│ Call: DiffFetcher.FetchPRDetails             │
│  - call provider API for diff, metadata     │
│  - compute diff_hash                        │
│  - count changed lines                      │
│  - if changed lines > 5,000 → set           │
│    diff_too_large flag                      │
└──────────────────────┬──────────────────────┘
                       │
                       ▼
               ┌──────────────┐
               │diff_too_large?│
               └──────┬───────┘
                 yes  │  no
          ┌───────────┘───────────┐
          ▼                       ▼
┌──────────────────┐  ┌─────────────────────────────────────────────┐
│ Call:            │  │ Call: DiffFetcher.CheckPreviousReview        │
│ PostReview.Post: │  │  - query DB for prior review of same PR     │
│ "PR too large    │  │  - if diff_hash unchanged and not force     │
│  for automated   │  │    → skip, exit                             │
│  review"         │  │  - if prior review exists → load previous   │
│ → exit           │  │    comments from DB (don't repeat)          │
└──────────────────┘  └──────────────────────┬──────────────────────┘
                                             │
                                             ▼
                      ┌─────────────────────────────────────────────┐
                      │ Call: DiffFetcher.FetchRepoRules             │
                      │  - resolve .review-rules.yaml from PR head  │
                      │    (target branch + PR diff applied):       │
                      │    • if PR modifies .review-rules.yaml →    │
                      │      use the version from the PR head ref   │
                      │      and set rules_modified=true             │
                      │    • otherwise → use the target branch copy │
                      │    • fetched via provider API (GetFileContent│
                      │      at PR head ref), no local clone needed │
                      │  - parse ignore globs + custom instructions │
                      │  - filter diff: remove files matching       │
                      │    ignore patterns                          │
                      │  - if no reviewable files remain → skip     │
                      └──────────────────────┬──────────────────────┘
                                             │
                                             ▼
                      ┌─────────────────────────────────────────────┐
                      │ Call: RepoSyncer.SyncRepo                   │
                      │  - pull latest target branch to local clone │
                      └──────────────────────┬──────────────────────┘
                                             │
                                             ▼
                      ┌─────────────────────────────────────────────┐
                      │ Call: Indexer.IndexRepo                      │
                      │  - check BranchIndex for target branch       │
                      │  - if unchanged → skip indexing             │
                      │  - if target ≠ primary branch and no        │
                      │    collection exists → clone primary branch │
                      │    collection, reindex only differing files │
                      │  - otherwise full incremental index         │
                      │  - upsert BranchIndex record                │
                      └──────────────────────┬──────────────────────┘
                                             │
                                             ▼
                      ┌─────────────────────────────────────────────┐
                      │ Call: Reviewer.RunReview                     │
                      │  - invoke Pydantic AI agent with:           │
                      │    • filtered PR diff                       │
                      │    • previous comments (if any)             │
                      │    • custom instructions (DB + repo rules)  │
                      │    • MCP tools: search + file reader        │
                      │  - returns structured review output         │
                      └──────────────────────┬──────────────────────┘
                                             │
                                             ▼
                      ┌─────────────────────────────────────────────┐
                      │ Call: PostReview.Post                        │
                      │  - if dry_run → store results, skip posting │
                      │  - if rules_modified → post warning comment:│
                      │    "This PR modifies .review-rules.yaml.    │
                      │     Review rules have been adjusted          │
                      │     accordingly."                            │
                      │  - post summary comment (first review only; │
                      │    on re-reviews the original stays as-is)  │
                      │  - post new inline comments via provider API │
                      │    (idempotent: each comment is stored in DB │
                      │    before posting; on retry, comments with   │
                      │    a provider_comment_id are skipped)        │
                      │  - update existing comments when the LLM    │
                      │    output includes an update action with a  │
                      │    previous comment ID                      │
                      │  - previous comments not mentioned by the   │
                      │    LLM in the new review are left as-is     │
                      │    (assumed still relevant or already        │
                      │    resolved by the author)                  │
                      │  - store review + comments (with body) in DB│
                      │  - log activity                             │
                      └─────────────────────────────────────────────┘
```

### 5.1 Background Indexing

Primary branch indexing runs **independently** from the PR review pipeline to keep the vector index warm and avoid blocking reviews with expensive full-index operations.

**Virtual Object:** `IndexMainBranch`
**Key:** `<repo_id>`
**Handler:** `Run` (exclusive — prevents concurrent indexing of the same repo)
**Trigger:** Push to primary branch (via webhook), or self-scheduled periodic execution (configurable, default: every 6 hours).

```
┌─────────────────────────────────────────────┐
│ Call: RepoSyncer.SyncRepo                    │
│  - pull latest primary branch               │
└──────────────────────┬──────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────┐
│ Call: Indexer.IndexRepo                      │
│  - incremental index of primary branch      │
│  - upsert BranchIndex record in DB          │
└──────────────────────┬──────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────┐
│ Schedule next run                            │
│  - ctx.send(self, delay=6h)                 │
│  - durable: survives restarts               │
└─────────────────────────────────────────────┘
```

This ensures that when a PR targets the primary branch, the index is already up-to-date and the `IndexRepo` step in the PR review pipeline becomes a fast no-op.

**Self-scheduling pattern:** Restate does not have built-in cron scheduling. Instead, the handler sends a delayed invocation to itself after completing. The delay is durable — if the service restarts, Restate fires the invocation at the correct time. The initial invocation is triggered when a repo is enabled for review.

### 5.2 PR Reconciliation

Periodic reconciliation catches missed webhooks (crashes, misconfiguration, network issues) without requiring an inbox table or delivery guarantees on the webhook path.

**Virtual Object:** `PRReconciliation`
**Key:** `<repo_id>`
**Handler:** `Run` (exclusive)
**Schedule:** Self-scheduled, configurable interval (default: every 5 minutes).

```
┌─────────────────────────────────────────────────┐
│ List open PRs via provider API                   │
│  - for each PR, fetch head commit SHA            │
└──────────────────────┬──────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────┐
│ Reconcile                                        │
│  - compare open PRs against PRReview records    │
│  - skip PRs opened before the repo was enabled  │
│    (repo.review_enabled_at timestamp)            │
│  - for draft PRs → upsert PRReview with          │
│    status=draft (no review dispatched)           │
│  - for each unreviewed or stale PR (head SHA ≠  │
│    last reviewed diff's head SHA):               │
│    → cancel + submit PRReview invocation         │
└──────────────────────┬──────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────┐
│ Schedule next run                                │
│  - ctx.send(self, delay=5m)                     │
│  - durable: survives restarts                   │
└─────────────────────────────────────────────────┘
```

**Key behaviors:**
- **Pre-existing PRs are skipped:** When review is enabled for a repo, a one-time snapshot creates `PRReview` records with `status=skipped` for all currently open PRs. The reconciliation loop then naturally ignores these PRs (they already have a review record). Admins can trigger reviews for skipped PRs explicitly via the Admin Console.
- **Idempotent:** If a webhook already triggered the review, `CheckPreviousReview` in the PR review pipeline detects the matching diff hash and skips — no duplicate work.
- **Single instance guaranteed:** The Virtual Object's exclusive handler ensures only one reconciliation runs per repo at a time.

## 6. Provider Abstraction

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

## 7. Webhook Handling

- Each provider instance gets a unique webhook URL: `/webhooks/<provider-id>`
- Webhook secret is generated on provider creation and displayed to the admin for manual configuration on GitHub/GitLab
- API Server validates webhook signatures per provider type
- Supported events:
  - **PR open/update/synchronize** → cancels any in-flight `PRReview` invocation for the same PR, then submits a new one; draft PRs create a `PRReview` record with `status=draft` instead (no review dispatched)
  - **PR marked ready for review** → clears the `draft` status, cancels + submits `PRReview` invocation
  - **Push to primary branch** → submits `IndexMainBranch` invocation
  - All other events are ignored
- The webhook handler calls Restate's HTTP ingress directly (cancel + submit). No in-process queuing.
- Concurrency is managed by Virtual Object key exclusivity (see section 3.4)

## 8. Custom Instructions

Instructions come from **two sources**, merged at review time:

### 8.1 Organization Instructions (Admin Console)
- Organization admins write free-text review instructions via the Admin Console
- Each instruction has optional filter rules:
  - Repository filter (specific repos or all)
  - File pattern filter (globs, e.g., `*.go`, `src/**/*.ts`)
  - Language filter
- At review time, applicable instructions are resolved based on the PR's repo and changed files

### 8.2 Repository-Level Rules (`.review-rules.yaml`)
- Developers can commit a `.review-rules.yaml` file to the repository root
- This file is read from the **PR head ref** (i.e., the result of target branch + PR changes) during the `FetchRepoRules` handler. This means a PR that adds or modifies `.review-rules.yaml` will be reviewed using the updated rules — the author's intent is respected immediately.
- Fetched via the provider API (`GetFileContent` at the PR head commit SHA), so it does not depend on the local repo clone being synced yet
- Format:

```yaml
# .review-rules.yaml

# Additional review instructions injected into the LLM prompt
instructions:
  - "Always check for proper error handling in Go code"
  - "Ensure all public API endpoints have authentication middleware"
  - "Flag any direct SQL queries — prefer using the query builder"

# Files matching these globs are excluded from review entirely.
# The reviewer will not see or comment on these files.
ignore:
  - "vendor/**"
  - "**/*.generated.go"
  - "**/*.pb.go"
  - "**/node_modules/**"
  - "**/__snapshots__/**"
  - "*.min.js"
  - "*.min.css"
```

- **Ignore rules** filter the diff before it reaches the reviewer — ignored files are stripped from the diff entirely, not just suppressed in output
- **Instructions** from the YAML are appended to the organization-level instructions in the system prompt
- If the file does not exist, no repo-level rules are applied (silent no-op)

## 9. Review Feedback

Developers can provide feedback on individual review comments to measure and improve review quality over time.

- **Feedback mechanism:** Developers react to review comments on the provider (e.g., GitHub reactions). A periodic sync fetches reactions and maps them to `ReviewFeedback` records in the DB.
- **Dashboard:** The Admin Console shows aggregate feedback metrics — acceptance rate per repo, per instruction set, and over time.
- **Use cases:**
  - Identify low-quality instruction sets that produce unhelpful comments
  - Track review quality trends as instructions are tuned
  - Provide data for future fine-tuning or prompt optimization

## 10. Observability

- **Structured logging:** All components emit structured JSON logs (Go: `slog`; Python: `structlog`). Each log entry includes `org_id`, `repo_id`, `pr_number`, and `invocation_id` for correlation.
- **Metrics:** Exposed via Prometheus endpoints on each service:
  - Review latency (end-to-end and per-step)
  - LLM token usage (prompt + completion tokens per review)
  - Indexing duration and document count
  - Diff size distribution
  - Invocation success/failure rates
- **Tracing:** OpenTelemetry spans across Go and Python services. Restate supports OpenTelemetry trace context propagation across service boundaries.
- **Restate UI:** Built-in web UI for inspecting running invocations, viewing execution timelines and call chains, debugging stuck or failed invocations, and restarting failed invocations directly.
- **Alerting:** Recommended Prometheus alerting rules shipped in `deploy/alerts.yml`:
  - Invocation failure rate > 10% over 15 minutes
  - Review latency p95 > 10 minutes
  - OpenRouter API error rate spike
  - Qdrant health check failures

## 11. Schema Migrations

Database schema is managed with **golang-migrate**. Migration files live in `migrations/` using the sequential naming convention (`000001_init.up.sql` / `000001_init.down.sql`).

- **On startup:** The API Server runs `migrate.Up()` automatically before accepting traffic. This ensures the schema is always current after an upgrade.
- **CLI:** `migrate` CLI is available in the Docker image for manual operations (`migrate -path migrations -database $DATABASE_URL up/down/version`).
- **CI:** A test step verifies that migrations apply cleanly to an empty database and that each `down` migration cleanly reverses its corresponding `up`.
- **Versioning:** Each migration is idempotent where possible (`CREATE TABLE IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS`). Destructive changes (column drops, type changes) get their own migration with a clear description.

## 12. Deployment

Self-hosted. All components run on the customer's infrastructure. A `docker-compose.yml` will define:
- API Server
- Go Service (registers all Go handlers with Restate)
- Python Indexer Service
- Python Reviewer Service
- Restate Server (single binary, embedded storage)
- PostgreSQL
- Qdrant

Configuration via environment variables / `.env` file.
