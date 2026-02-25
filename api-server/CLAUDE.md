# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Go HTTP server exposing the admin API via ConnectRPC. Runs database migrations on startup, manages providers/repos, and triggers reviews via Restate.

## Commands

```bash
# Build
cd api-server && go build ./cmd/server

# Run tests
cd api-server && go vet ./... && go test ./...

# Run a single test
cd api-server && go test ./internal/handler/ -run TestWebhookHandler_ValidToken

# Run via Docker (from repo root)
docker compose up api-server

# Generate protobuf code (from repo root)
make proto
```

## Environment Variables

- `DATABASE_URL` — PostgreSQL connection string (required)
- `ENCRYPTION_KEY` — 32-byte hex-encoded AES-256-GCM key (required)
- `RESTATE_INGRESS_URL` — Restate ingress URL for fire-and-forget review submissions (required)
- `RESTATE_ADMIN_URL` — Restate admin API URL for cancelling invocations (required)
- `LISTEN_ADDR` — HTTP listen address (default `:8090`)

## Architecture

**Module:** `ai-reviewer/api-server` (Go 1.24, `go.mod` with `replace` directive to `../gen/go`)

**Entry point:** `cmd/server/main.go` — loads config, runs embedded migrations (`migrations/embed.go` with `//go:embed`), connects to PostgreSQL via pgx pool, registers ConnectRPC handlers, starts h2c HTTP server.

### Internal Packages

- **`config/`** — env var loading
- **`crypto/`** — AES-256-GCM encrypt/decrypt (copy of `go-services/internal/crypto/`, keep in sync)
- **`db/`** — pgx pool wrapper and hand-written queries
- **`handler/`** — ConnectRPC handler implementations:
  - `provider.go` — `CreateProvider` (validates GitLab, encrypts token, syncs repos in a single transaction), `ListProviders`, `DeleteProvider` (soft-delete)
  - `repo.go` — `ListRepos`, `EnableReview`, `DisableReview`
  - `review.go` — `TriggerReview` (creates review_run row, fires PRReview via Restate `/send`), `GetReviewRun`
  - `webhook.go` — `POST /webhooks/{provider_id}` handler for GitLab MR events. Validates `X-Gitlab-Token`, filters non-MR/non-reviewable actions, handles draft→ready transitions, cancels existing invocations (debounce), dispatches via Restate. Uses `WebhookStore` and `RestateDispatcher` interfaces for testability.
  - `mapper.go` — DB row to protobuf response mapping
- **`provider/`** — `GitProvider` interface + GitLab implementation (copy of `go-services/internal/provider/`, keep in sync). Only `ListRepos` is used here.
- **`restate/`** — HTTP client for Restate ingress and admin API. `SendPRReview` posts fire-and-forget to `/PRReview/{key}/Run/send` (202). `CancelInvocation` patches `/invocations/{id}/cancel` via admin API (404 silently ignored).

### Migrations

SQL files in `migrations/` managed by golang-migrate, embedded in the binary:
- `000001_init` — organizations, providers, repositories, review_runs, review_comments
- `000002_review_comments_posted` — adds `provider_comment_id` and `posted` to review_comments, `summary` to review_runs
- `000003_provider_soft_delete` — adds `deleted_at` to providers, changes FK constraint
- `000004_webhook_secret` — adds `webhook_secret` to providers
- `000005_restate_invocation_id` — adds `restate_invocation_id` to review_runs
- `000006_diff_hash` — adds `skipped` status to review_status enum and `diff_hash` to review_runs
- `000007_draft_status` — adds `draft` status to review_status enum

### HTTP Endpoints

- ConnectRPC services: `ProviderService`, `RepoService`, `ReviewService` (generated paths from protobuf)
- `POST /webhooks/{provider_id}` — GitLab webhook receiver
- `GET /healthz` — health check

### Key Design Decisions

- **Programmatic migrations on startup** — no separate migrate container needed
- **h2c wrapper** — supports both gRPC (HTTP/2) and Connect JSON (HTTP/1.1) clients without TLS
- **CreateProvider is atomic** — calls GitLab `ListRepos` first, then wraps provider insert + repo upserts in a single transaction
- **RunID created at API layer** — `TriggerReview` creates the review_run row before dispatching to Restate, so the caller gets a valid run ID immediately
- **Soft-delete for providers** — preserves audit trail; all queries filter `WHERE deleted_at IS NULL`
- **Webhook handler uses interfaces** — `WebhookStore` and `RestateDispatcher` interfaces enable unit testing with stubs (no DB/Restate needed)
- **Debounce via cancel-and-replace** — webhook handler cancels active Restate invocation (looked up via `restate_invocation_id` on the latest review_run) before dispatching a new one for the same MR. Cancel is best-effort: failure is logged but does not block dispatch.
- **Invocation ID tracking** — `SendPRReview` returns the Restate invocation ID from the `202 Accepted` response. Stored on `review_runs.restate_invocation_id` for subsequent cancel-on-new-push.
- **Draft MR tracking** — draft MRs create a `status=draft` review run (no Restate dispatch); draft→ready transition converts it to `pending` and dispatches. `TransitionDraftToReview` is idempotent (updates at most one row).
- **Webhook token validation** — uses `crypto/subtle.ConstantTimeCompare` to prevent timing attacks. `webhook_secret` column is nullable for backward compatibility with pre-migration providers.

### Protobuf

API definitions in `proto/api/v1/` (provider.proto, repo.proto, review.proto). Generated Go code in `gen/go/`, imported as `ai-reviewer/gen`. Code generation: `make proto` (uses buf).
