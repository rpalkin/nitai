# api-server — CLAUDE.md

Go HTTP server exposing the admin API via ConnectRPC. Runs database migrations on startup, manages providers/repos, and triggers reviews via Restate.

## Commands

```bash
# Build
cd api-server && go build ./cmd/server

# Run tests
cd api-server && go vet ./... && go test ./...

# Run via Docker (from repo root)
docker compose up api-server

# Generate protobuf code (from repo root)
make proto
```

## Environment Variables

- `DATABASE_URL` — PostgreSQL connection string (required)
- `ENCRYPTION_KEY` — 32-byte hex-encoded AES-256-GCM key (required)
- `RESTATE_INGRESS_URL` — Restate ingress URL for fire-and-forget review submissions (required)
- `LISTEN_ADDR` — HTTP listen address (default `:8080`)

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
  - `mapper.go` — DB row to protobuf response mapping
- **`provider/`** — `GitProvider` interface + GitLab implementation (copy of `go-services/internal/provider/`, keep in sync). Only `ListRepos` is used here.
- **`restate/`** — HTTP client for Restate ingress. `SendPRReview` posts to `POST {baseURL}/PRReview/{key}/Run/send` (fire-and-forget, returns 202)

### Migrations

SQL files in `migrations/` managed by golang-migrate, embedded in the binary:
- `000001_init` — organizations, providers, repositories, review_runs, review_comments
- `000002_review_comments_posted` — adds `provider_comment_id` and `posted` to review_comments, `summary` to review_runs
- `000003_provider_soft_delete` — adds `deleted_at` to providers, changes FK constraint

### Key Design Decisions

- **Programmatic migrations on startup** — no separate migrate container needed
- **h2c wrapper** — supports both gRPC (HTTP/2) and Connect JSON (HTTP/1.1) clients without TLS
- **CreateProvider is atomic** — calls GitLab `ListRepos` first, then wraps provider insert + repo upserts in a single transaction
- **RunID created at API layer** — `TriggerReview` creates the review_run row before dispatching to Restate, so the caller gets a valid run ID immediately
- **Soft-delete for providers** — preserves audit trail; all queries filter `WHERE deleted_at IS NULL`

### Protobuf

API definitions in `proto/api/v1/` (provider.proto, repo.proto, review.proto). Generated Go code in `gen/go/`, imported as `ai-reviewer/gen`. Code generation: `make proto` (uses buf).
