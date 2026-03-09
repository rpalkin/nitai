# go-services — CLAUDE.md

Go Restate service handlers that orchestrate the PR review pipeline. Registers five services with Restate: `DiffFetcher`, `PostReview`, `PRReview` (Virtual Object), `RepoSyncer`, and `IndexMainBranch` (Virtual Object).

## Commands

```bash
# IMPORTANT: gen/go/ is gitignored — run `make proto` from repo root before first build

# Build
go build ./cmd/worker

# Run all tests with vet + build checks (preferred)
../tests/unit.sh

# Run tests directly
go vet ./... && go test ./...

# Run a single test
go test ./internal/provider/gitlab/ -run TestGetMRDiff

# Run integration tests against real GitLab (requires env vars)
GITLAB_URL=... GITLAB_TOKEN=... go test -tags=integration ./internal/provider/gitlab/

# Run via Docker (from repo root)
docker compose up worker
```

## Environment Variables

- `DATABASE_URL` — PostgreSQL connection string (required)
- `ENCRYPTION_KEY` — 32-byte hex-encoded AES-256-GCM key (required)
- `WORKER_ADDR` — Restate HTTP listen address (default `:9080`)
- `DEBOUNCE_TIMEOUT` — Duration string for PRReview debounce sleep (default `3m`, e2e uses `5s`)

## Architecture

**Module:** `ai-reviewer/go-services` (Go 1.24, `go.mod` with `replace` directive to `../gen/go`)

**Entry point:** `cmd/worker/main.go` — loads config, connects to PostgreSQL via pgx, creates service instances, registers them with Restate SDK (`restate.Reflect`), starts Restate HTTP server.

### Restate Services

| Service | Type | Handler | Purpose |
|---|---|---|---|
| `DiffFetcher` | Service | `FetchPRDetails` | Fetches MR diff + metadata from GitLab. Reads provider credentials from DB (not passed in request). |
| `PostReview` | Service | `Post` | Posts summary comment + inline comments to GitLab MR. Idempotent via `provider_comment_id` check. |
| `PRReview` | Virtual Object | `Run` (exclusive) | Orchestrates the full Phase 2 pipeline: debounce → fetch → dedup → draft guard → SyncRepo → IndexRepo → Reviewer (with tools) → post comments. Keyed by `<repo_id>-<mr_number>`. |
| `RepoSyncer` | Service | `SyncRepo` | Maintains bare git clones on `/data/repos/<repo_id>/`. Clones on first call, fetches on subsequent. Returns `head_sha` of target branch. |
| `IndexMainBranch` | Virtual Object | `Run` (exclusive) | Background indexing loop for the primary branch. Keyed by `<repo_id>`. Syncs repo → indexes → self-schedules every 6h. Triggered by `EnableReview`. |

### Internal Packages

- **`config/`** — env var loading
- **`crypto/`** — AES-256-GCM encrypt/decrypt (copy of `api-server/internal/crypto/`, keep in sync)
- **`db/`** — pgx pool wrapper + hand-written query functions in `queries.go` (includes `GetBranchIndex`, `UpsertBranchIndex` for indexer state tracking)
- **`difffetcher/`** — `DiffFetcher` Restate service. Decrypts provider token, fetches MR details + diff via GitLab client. Also handles diff-hash dedup (compares HeadSHA against latest completed review).
- **`postreview/`** — `PostReview` Restate service. Posts summary + inline comments, updates DB with `provider_comment_id`.
- **`indexing/`** — shared types for indexer integration: `IndexRequest`, `IndexResult`, `SanitizeCollectionName`. Imported by both `prreview` and `indexmainbranch`.
- **`indexmainbranch/`** — `IndexMainBranch` Virtual Object. Background indexing loop for the primary branch. Keyed by `<repo_id>`. Syncs repo → indexes → self-schedules every 6h via `restate.ObjectSend` with delay. Triggered by `EnableReview`. Stops if `review_enabled` is false.
- **`prreview/`** — `PRReview` Virtual Object. Full Phase 2 orchestrator: smart debounce → DiffFetcher (details + dedup) → draft guard → RepoSyncer → Indexer (Python, cross-language) → Reviewer (Python, cross-language, with `repo_path`, `target_branch_sha`, `search_collection`) → PostReview. Uses Virtual Object state for debounce timing (`last_started_at`, `last_completed_at`).
- **`reposyncer/`** — `RepoSyncer` Restate service. Maintains bare git clones via `go-git` (pure Go, no shell-out). Handles clone, fetch, and remote URL updates. Returns `head_sha` for the target branch.
- **`provider/`** — `GitProvider` interface + GitLab REST API v4 implementation (hand-rolled HTTP, no go-gitlab library)
  - `provider.go` — interface definition + sentinel errors (`ErrNotFound`, `ErrUnauthorized`, `ErrForbidden`, `ErrRateLimited`)
  - `gitlab/gitlab.go` — implementation: `ListRepos`, `GetMRDiff`, `GetMRDetails`, `PostComment`, `PostInlineComment`
  - `gitlab/types.go` — response types
  - `gitlab/gitlab_test.go` — 15 unit tests using `httptest.NewServer`
  - `gitlab/integration_test.go` — tests against real GitLab (skipped without env vars)

### Key Design Decisions

- **Restate SDK v0.23.0** — handler registration via `restate.Reflect(struct)`, service type inferred from context parameter type
- **Cross-language calls** — `PRReview` calls `Reviewer.RunReview` (Python) via `restate.Service[O](ctx, "Reviewer", "RunReview")`. JSON field names must be snake_case matching Python models.
- **`repoRemoteID` is `string`** — provider-agnostic (GitHub uses `owner/repo`, GitLab uses numeric ID as string)
- **DiffFetcher reads credentials from DB** — encrypted token bytes stay out of Restate's durable journal
- **No retries in provider layer** — Restate handles all retry logic
- **`newProvider()` and `classifyProviderError()` duplicated** in difffetcher and postreview (~10 lines each, acceptable at this scale)
- **Smart debounce** — `PRReview.Run` uses Virtual Object state (`last_started_at`) to debounce: only sleeps when a previous invocation started recently (configurable via `DEBOUNCE_TIMEOUT`, default 3m). First webhook trigger proceeds immediately with zero delay.
- **HeadSHA as diff hash** — uses `details.HeadSHA` (git commit SHA) directly instead of SHA-256 of the diff content. Enables early exit without fetching the full diff: `GetMRDetails` is called before `GetMRDiff`.
- **Force flag** — `PRReviewRequest.Force` propagates to `FetchRequest.Force`. API-triggered reviews (`TriggerReview`) set `Force: true` (always run); webhook-triggered reviews leave it `false` (dedup enabled).
- **Diff-hash dedup** — if `HeadSHA` matches the latest completed review for the same repo+MR and `Force == false`, the run is marked `skipped` and exits early.
- **Draft guard** — `FetchResponse.Draft` (from `MRDetails.Draft`) is checked after fetch. If MR is still a draft, run is marked `draft` and exits early. Handles the race where MR was marked draft between webhook receipt and review execution.
- **Review statuses** — `review_status` enum: `pending`, `running`, `completed`, `failed`, `skipped` (dedup match), `draft` (MR is a draft)
- **RepoSyncer uses go-git** — pure Go git implementation (`github.com/go-git/go-git/v5`), no shell-out, no `gc.auto` concern. Auth via `http.BasicAuth` (not embedded in URL). Bare clones stored at `/data/repos/<repo_id>/`.
- **RepoSyncer is a plain Service** (not Virtual Object) — stateless, concurrent calls for the same repo are safe (go-git clone is atomic, fetch is read-safe).
- **PRReview.Run v2 pipeline** — debounce → fetch → dedup → draft guard → SyncRepo → IndexRepo → Reviewer (with `repo_path`, `target_branch_sha`, `search_collection`) → PostReview. SyncRepo failure is fatal; IndexRepo failure is non-fatal (review proceeds without search).
- **Branch index tracking** — `branch_indexes` table tracks `last_indexed_commit` per repo+branch. If already at `head_sha`, the `Indexer.IndexRepo` call is skipped entirely.
