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
- `JWT_SECRET` — Secret key for HS256 JWT signing (required, min 32 chars recommended)
- `RESTATE_INGRESS_URL` — Restate ingress URL for fire-and-forget review submissions (required)
- `RESTATE_ADMIN_URL` — Restate admin API URL for cancelling invocations (required)
- `LISTEN_ADDR` — HTTP listen address (default `:8090`)

## Architecture

**Module:** `ai-reviewer/api-server` (Go 1.24, `go.mod` with `replace` directive to `../gen/go`)

**Entry point:** `cmd/server/main.go` — loads config, runs embedded migrations (`migrations/embed.go` with `//go:embed`), connects to PostgreSQL via pgx pool, registers ConnectRPC handlers, starts h2c HTTP server.

### Internal Packages

- **`auth/`** — JWT token signing/verification and ConnectRPC auth interceptor
  - `jwt.go` — HS256 token signing with 24h expiry, claims: user_id, org_id
  - `interceptor.go` — ConnectRPC unary interceptor with allow-list for unauthenticated endpoints (Register, Login)
- **`config/`** — env var loading
- **`activitylog/`** — fire-and-forget activity logging helper (`Log()` swallows errors to never fail the parent operation)
- **`db/`** — pgx pool wrapper and hand-written queries
- **`handler/`** — ConnectRPC handler implementations:
  - `auth.go` — `Register` (bcrypt password hashing, default org assignment), `Login` (credential verification), `GetMe` (authenticated user lookup)
  - `provider.go` — `CreateProvider` (validates GitLab, encrypts token, syncs repos in a single transaction), `ListProviders`, `DeleteProvider` (soft-delete)
  - `repo.go` — `ListRepos`, `EnableReview`, `DisableReview`
  - `review.go` — `TriggerReview` (creates review_run row, fires PRReview via Restate `/send`), `GetReviewRun`
  - `instruction.go` — `InstructionService` CRUD + `ResolveInstructions` (filters by repo_filter, file_pattern_filter using `filepath.Match`)
  - `activity.go` — `ActivityHandler` with `ListActivityLogs` (paginated, filterable by repo_id and event_type)
  - `webhook.go` — `POST /webhooks/{provider_id}` handler for GitLab MR events. Validates `X-Gitlab-Token`, filters non-MR/non-reviewable actions, handles draft→ready transitions, cancels existing invocations (debounce), dispatches via Restate. Uses `WebhookStore` and `RestateDispatcher` interfaces for testability.
  - `mapper.go` — DB row to protobuf response mapping
- **`restate/`** — HTTP client for Restate ingress and admin API. `SendPRReview` posts fire-and-forget to `/PRReview/{key}/Run/send` (202). `CancelInvocation` patches `/invocations/{id}/cancel` via admin API (404 silently ignored).

### External Dependencies

- **`ai-reviewer/lib`** — Shared Go library providing `crypto/` (AES-256-GCM encrypt/decrypt) and `provider/gitlab` (GitLab API client) packages. Imported via `replace` directive in `go.mod`.

### Migrations

SQL files in `migrations/` managed by golang-migrate, embedded in the binary:
- `000001_init` — organizations, providers, repositories, review_runs, review_comments
- `000002_review_comments_posted` — adds `provider_comment_id` and `posted` to review_comments, `summary` to review_runs
- `000003_provider_soft_delete` — adds `deleted_at` to providers, changes FK constraint
- `000004_webhook_secret` — adds `webhook_secret` to providers
- `000005_restate_invocation_id` — adds `restate_invocation_id` to review_runs
- `000006_diff_hash` — adds `skipped` status to review_status enum and `diff_hash` to review_runs
- `000007_draft_status` — adds `draft` status to review_status enum
- `000008_branch_indexes` — creates `branch_indexes` table (repo_id, branch, last_indexed_commit, collection_name, updated_at) with unique constraint on (repo_id, branch)
- `000009_default_branch` — adds `default_branch TEXT NOT NULL DEFAULT 'main'` to repositories
- `000010_users` — creates `users` table (id, org_id, email, password_hash) with unique email constraint
- `000011_review_instructions` — creates `review_instructions` table (id, org_id, name, content, repo_filter UUID[], file_pattern_filter TEXT[], enabled, timestamps)
- `000012_conflicts_status` — adds `conflicts` value to review_status enum
- `000013_activity_logs` — creates `activity_logs` table (id, org_id, repo_id nullable, actor_id nullable, event_type, details JSONB, created_at) with indexes on (org_id, created_at), (org_id, event_type), (repo_id)
- `000014_dry_run` — adds `dry_run BOOLEAN NOT NULL DEFAULT false` to review_runs

### HTTP Endpoints

- ConnectRPC services: `AuthService`, `ProviderService`, `RepoService`, `ReviewService`, `InstructionService`, `ActivityService` (generated paths from protobuf)
  - `AuthService/Register` — Create new user account (unauthenticated)
  - `AuthService/Login` — Authenticate and get JWT (unauthenticated)
  - `AuthService/GetMe` — Get current authenticated user
  - `InstructionService/CreateInstruction`, `ListInstructions`, `UpdateInstruction`, `DeleteInstruction` — CRUD for org-scoped review instructions
  - `InstructionService/ResolveInstructions` — Returns applicable instructions given repo_id + changed_files
  - `ActivityService/ListActivityLogs` — Paginated activity log query with optional repo_id and event_type filters
- `POST /webhooks/{provider_id}` — GitLab webhook receiver (unauthenticated)
- `GET /healthz` — health check (unauthenticated)

### Key Design Decisions

- **Programmatic migrations on startup** — no separate migrate container needed
- **h2c wrapper** — supports both gRPC (HTTP/2) and Connect JSON (HTTP/1.1) clients without TLS
- **CreateProvider is atomic** — calls GitLab `ListRepos` first, then wraps provider insert + repo upserts in a single transaction
- **RunID created at API layer** — `TriggerReview` creates the review_run row before dispatching to Restate, so the caller gets a valid run ID immediately
- **Soft-delete for providers** — preserves audit trail; all queries filter `WHERE deleted_at IS NULL`
- **Webhook handler uses interfaces** — `WebhookStore` and `RestateDispatcher` interfaces enable unit testing with stubs (no DB/Restate needed)
- **Debounce via cancel-and-replace** — webhook handler cancels active Restate invocation (looked up via `restate_invocation_id` on the latest review_run) before dispatching a new one for the same MR. Cancel is best-effort: failure is logged but does not block dispatch.
- **Invocation ID tracking** — `SendPRReview` returns the Restate invocation ID from the `202 Accepted` response. Stored on `review_runs.restate_invocation_id` for subsequent cancel-on-new-push.
- **Single run per webhook** — webhook handler creates the run first (`CreateReviewRun`), passes `RunID` to `SendPRReview`, then updates with `UpdateReviewRunInvocationID`. This ensures exactly one DB run per webhook-triggered review (the worker reuses the existing run instead of creating a second one).
- **Draft MR tracking** — draft MRs create a `status=draft` review run (no Restate dispatch); draft→ready transition converts it to `pending` and dispatches. `TransitionDraftToReview` is idempotent (updates at most one row).
- **Webhook token validation** — uses `crypto/subtle.ConstantTimeCompare` to prevent timing attacks. `webhook_secret` column is nullable for backward compatibility with pre-migration providers.
- **JWT-based authentication** — All ConnectRPC endpoints (except Register, Login) require `Authorization: Bearer <token>`. Token contains user_id and org_id claims, signed with HS256, 24h expiry. Webhook and healthz endpoints are plain HTTP and bypass ConnectRPC interceptors.
- **Single organization model** — All users belong to the default organization (created in migration 000001). Multi-tenancy can be added later.

### Protobuf

API definitions in `proto/api/v1/` (auth.proto, provider.proto, repo.proto, review.proto, instruction.proto, activity.proto). Generated Go code in `gen/go/`, imported as `ai-reviewer/gen`. Code generation: `make proto` (uses buf).
