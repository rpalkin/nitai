# go-services — CLAUDE.md

Go Restate service handlers that orchestrate the PR review pipeline. Registers three services with Restate: `DiffFetcher`, `PostReview`, and `PRReview` (Virtual Object).

## Commands

```bash
# Build
cd go-services && go build ./cmd/worker

# Run tests (includes GitLab client unit tests)
cd go-services && go vet ./... && go test ./...

# Run integration tests against real GitLab (requires env vars)
cd go-services && GITLAB_URL=... GITLAB_TOKEN=... go test -tags=integration ./internal/provider/gitlab/

# Run via Docker (from repo root)
docker compose up worker
```

## Environment Variables

- `DATABASE_URL` — PostgreSQL connection string (required)
- `ENCRYPTION_KEY` — 32-byte hex-encoded AES-256-GCM key (required)
- `WORKER_ADDR` — Restate HTTP listen address (default `:9080`)

## Architecture

**Module:** `ai-reviewer/go-services` (Go 1.24, `go.mod` with `replace` directive to `../gen/go`)

**Entry point:** `cmd/worker/main.go` — loads config, connects to PostgreSQL via pgx, creates service instances, registers them with Restate SDK (`restate.Reflect`), starts Restate HTTP server.

### Restate Services

| Service | Type | Handler | Purpose |
|---|---|---|---|
| `DiffFetcher` | Service | `FetchPRDetails` | Fetches MR diff + metadata from GitLab. Reads provider credentials from DB (not passed in request). |
| `PostReview` | Service | `Post` | Posts summary comment + inline comments to GitLab MR. Idempotent via `provider_comment_id` check. |
| `PRReview` | Virtual Object | `Run` (exclusive) | Orchestrates the full pipeline: create run → fetch diff → call Reviewer → store results → post comments. Keyed by `<repo_id>-<mr_number>`. |

### Internal Packages

- **`config/`** — env var loading
- **`crypto/`** — AES-256-GCM encrypt/decrypt (copy of `api-server/internal/crypto/`, keep in sync)
- **`db/`** — pgx pool wrapper + 7 hand-written query functions in `queries.go`
- **`difffetcher/`** — `DiffFetcher` Restate service. Decrypts provider token, fetches diff via GitLab client.
- **`postreview/`** — `PostReview` Restate service. Posts summary + inline comments, updates DB with `provider_comment_id`.
- **`prreview/`** — `PRReview` Virtual Object. Orchestrator calling DiffFetcher → Reviewer (Python, cross-language) → PostReview.
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
