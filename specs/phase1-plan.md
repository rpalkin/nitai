# Phase 1 — MVP: Diff-Only GitLab Reviewer — Implementation Plan ✓ Done

## Overview

End-to-end review loop: manual API trigger in, comments out on self-hosted GitLab. Restate for durable orchestration. No webhooks, no semantic search, no repo cloning, no admin UI.

**Status:** All 6 subphases complete. See `later.md` for known issues and deferred items.

## Decisions

- **Token encryption:** AES-256-GCM from day 1 (`ENCRYPTION_KEY` env var)
- **Go structure:** Separate Go modules per service (`api-server/`, `go-services/`)
- **Binaries:** Two separate binaries and Docker containers (api-server + go-services)
- **API protocol:** ConnectRPC (connect-go + protobuf) from day 1
- **Review LLM:** OpenRouter with configurable model via `REVIEW_MODEL` env var
- **Diff size limit:** Deferred to Phase 2

## Directory Structure (target)

```
ai-reviewer/
├── proto/                     # Protobuf definitions (shared)
│   └── api/v1/
│       ├── provider.proto
│       ├── repo.proto
│       └── review.proto
├── api-server/                # Go module — HTTP server (ConnectRPC)
│   ├── go.mod
│   ├── cmd/
│   │   └── server/main.go
│   ├── internal/
│   │   ├── db/               # sqlc-generated code + queries
│   │   ├── crypto/           # AES-256-GCM encrypt/decrypt
│   │   ├── handler/          # ConnectRPC handler implementations
│   │   ├── restate/          # Restate ingress client (trigger reviews)
│   │   └── config/           # Env var config
│   ├── migrations/           # golang-migrate SQL files
│   └── Dockerfile
├── go-services/               # Go module — Restate service handlers
│   ├── go.mod
│   ├── cmd/
│   │   └── worker/main.go
│   ├── internal/
│   │   ├── provider/         # GitProvider interface + GitLab impl
│   │   ├── difffetcher/      # DiffFetcher Restate service
│   │   ├── postreview/       # PostReview Restate service
│   │   ├── prreview/         # PRReview Virtual Object (orchestrator)
│   │   └── db/               # Shared DB access (review runs, comments)
│   └── Dockerfile
├── reviewer/                  # Python module — Restate service
│   ├── pyproject.toml
│   ├── reviewer/
│   │   ├── __init__.py
│   │   ├── service.py        # Restate handler: RunReview
│   │   ├── agent.py          # Pydantic AI agent definition
│   │   ├── prompt.py         # System/user prompt templates
│   │   └── models.py         # Pydantic models (input/output)
│   └── Dockerfile
├── indexer/                   # (existing, untouched in Phase 1)
├── search-mcp/                # (existing, untouched in Phase 1)
├── docker-compose.yml         # Updated for Phase 1 stack
├── .env.example               # Updated with new vars
└── specs/
```

---

## Subphase 1.1 — Foundation: Database, Crypto & Protobuf ✓ Done

**Goal:** PostgreSQL schema, migration infrastructure, token encryption, and protobuf API definitions.

### Tasks

1. **PostgreSQL schema & migrations**
   - Set up `api-server/migrations/` with golang-migrate
   - `000001_init.up.sql` — create tables:
     - `organizations` (id UUID PK, name, created_at) — single org for MVP, but schema is ready
     - `providers` (id UUID PK, org_id FK, type enum, api_url, encrypted_token bytea, created_at)
     - `repositories` (id UUID PK, org_id FK, provider_id FK, remote_id, name, full_name, default_branch, review_enabled bool, created_at)
     - `review_runs` (id UUID PK, repo_id FK, mr_number int, status enum [pending/running/completed/failed], summary text, created_at, completed_at)
     - `review_comments` (id UUID PK, review_run_id FK, file_path, line int, body text, provider_comment_id text nullable, posted bool default false, created_at)
   - Corresponding `000001_init.down.sql`

2. **Token encryption module**
   - `api-server/internal/crypto/` — `Encrypt(plaintext, key) -> ciphertext` and `Decrypt(ciphertext, key) -> plaintext`
   - AES-256-GCM with random nonce prepended to ciphertext
   - Key loaded from `ENCRYPTION_KEY` env var (32-byte hex or base64)
   - Unit tests

3. **Protobuf definitions**
   - `proto/api/v1/provider.proto` — `ProviderService` with:
     - `CreateProvider` (api_url, token, type) -> provider
     - `ListProviders` -> providers
     - `DeleteProvider` (id) -> empty
   - `proto/api/v1/repo.proto` — `RepoService` with:
     - `ListRepos` (provider_id) -> repos
     - `EnableReview` (repo_id) -> repo
     - `DisableReview` (repo_id) -> repo
   - `proto/api/v1/review.proto` — `ReviewService` with:
     - `TriggerReview` (repo_id, mr_number) -> review_run
     - `GetReviewRun` (id) -> review_run with comments
   - Generate Go code with `buf` or `protoc-gen-go` + `protoc-gen-connect-go`

4. **Makefile / script for codegen**
   - `buf.yaml` + `buf.gen.yaml` for protobuf generation
   - Generated code output to `api-server/gen/` and `go-services/gen/`

### Deliverable
- `docker compose up` starts PostgreSQL with migrations applied
- Proto-generated Go code compiles
- Encryption round-trips in tests

### Implementation notes

**What changed from the plan:**

- **Generated code location:** Plan said output to `api-server/gen/` and `go-services/gen/`. Implemented as a single `gen/go/` directory at repo root with its own `go.mod` (`module ai-reviewer/gen`). Both `api-server` and `go-services` import it via a `replace` directive. This avoids duplicating generated code and lets `go mod tidy` in `gen/go/` pull the exact runtime deps (protobuf, connect) without coupling them to each app module.

- **buf remote plugins over local protoc:** Used `buf.build/protocolbuffers/go` and `buf.build/connectrpc/go` remote BSR plugins — no local `protoc` or plugin binaries needed. `make proto` is `buf lint && buf generate && cd gen/go && go mod tidy`.

- **Schema simplifications deferred to later subphases:**
  - `repositories` omits `org_id` (reachable via `provider → org`) and `default_branch` (not needed until Phase 2 indexing).
  - `review_runs` omits `summary text` and `completed_at`; uses `updated_at` instead. Summary text will live in a review comment row or be added when the reviewer service is wired up.
  - `review_comments` omits `provider_comment_id` and `posted` (needed for `PostReview` idempotency in 1.3; will be added in a follow-up migration).
  - `providers` field renamed from `api_url` → `base_url` to match the proto field name.
  - `providers` adds a `name` text field (user-facing label for the provider).

- **migrate service uses a Compose profile:** The `migrate` service in `docker-compose.yml` is under `profiles: [migrate]` so it doesn't run on a plain `docker compose up`. Invoked explicitly via `docker compose --profile migrate run --rm migrate`. Will switch to programmatic `migrate.Up()` in subphase 1.5 when the api-server binary exists.

- **Hardcoded internal DB URL in migrate command:** `${DATABASE_URL}` in a Compose `command` array is expanded at parse time from the host `.env` (which has `localhost`). The migrate container needs the internal service hostname. Fixed by hardcoding `postgres://...@postgres:5432/...` directly in the command rather than going through the env var.

---

## Subphase 1.2 — GitLab Provider ✓ Done

**Goal:** Go implementation of the GitLab provider (subset of `GitProvider` interface needed for Phase 1).

### Tasks

1. **GitProvider interface**
   - `go-services/internal/provider/provider.go` — define interface:
     ```go
     type GitProvider interface {
         ListRepos(ctx context.Context) ([]Repo, error)
         GetMRDiff(ctx context.Context, repoRemoteID, mrNumber) (*MRDiff, error)
         GetMRDetails(ctx context.Context, repoRemoteID, mrNumber) (*MRDetails, error)
         PostComment(ctx context.Context, repoRemoteID, mrNumber, body) (*CommentResult, error)
         PostInlineComment(ctx context.Context, repoRemoteID, mrNumber, InlineComment) (*CommentResult, error)
     }
     ```
   - Common types: `Repo`, `MRDiff` (unified diff string + changed files list + changed line count), `MRDetails` (title, description, author, source_branch, target_branch, head_sha), `InlineComment` (file_path, line, body, new_line bool)

2. **GitLab implementation**
   - `go-services/internal/provider/gitlab/gitlab.go`
   - Uses GitLab REST API v4 (`/api/v4/...`)
   - `ListRepos` — paginated list of projects accessible by token (`GET /projects?membership=true`)
   - `GetMRDiff` — `GET /projects/:id/merge_requests/:mr/changes` → extract diffs, build unified diff string
   - `GetMRDetails` — `GET /projects/:id/merge_requests/:mr` → title, description, author, branches
   - `PostComment` — `POST /projects/:id/merge_requests/:mr/notes` → summary comment
   - `PostInlineComment` — `POST /projects/:id/merge_requests/:mr/discussions` with position payload (new line on diff)
   - Handle pagination, auth (Private-Token header), error mapping
   - Configurable base URL for self-hosted GitLab

3. **Integration test (optional, manual)**
   - A test that can be run against a real GitLab instance with a token (skipped in CI)

### Deliverable
- GitLab provider can list repos, fetch MR diffs/details, and post comments against a real self-hosted GitLab

### Implementation notes

**What changed from the plan:**

- **Hand-rolled HTTP client (no go-gitlab library):** The plan left library choice open. Decided against `go-gitlab` (~40k LOC wrapping the entire API surface) since we only need 5 endpoints. Stdlib `net/http` + `encoding/json` — zero new dependencies.

- **`repoRemoteID` typed as `string`, not `int`:** GitLab uses numeric project IDs but the interface is provider-agnostic (GitHub uses `owner/repo` strings). Keeping it `string` avoids a type mismatch when a GitHub provider is added in Phase 5. GitLab callers pass the numeric ID as a decimal string.

- **Sentinel errors as package-level `var` values:** `ErrNotFound`, `ErrUnauthorized`, `ErrForbidden`, `ErrRateLimited` defined in the `provider` package. HTTP status → sentinel mapping lives in `checkStatus` inside the gitlab package. Callers compare with `errors.Is`; no error wrapping is done on the sentinel itself so they remain directly comparable.

- **Diff reconstruction in `GetMRDiff`:** GitLab's `GET /merge_requests/:iid/changes` returns diff fragments without `diff --git` headers. We prepend the standard header, `--- a/...` / `+++ b/...` lines (substituting `/dev/null` for new/deleted files), and `new file mode` / `deleted file mode` lines. Line counting (`ChangedLines`) walks the raw diff fragment counting `+` and `-` lines, excluding `+++`/`---` file headers.

- **`getMRVersions` helper for `PostInlineComment`:** GitLab's discussion position payload requires `base_sha`, `head_sha`, and `start_sha`. These come from a separate `GET /merge_requests/:iid/versions` call. Extracted as an unexported `getMRVersions` method that returns the first (latest) version entry. The alternative — requiring callers to pass SHAs — would leak GitLab internals into the interface.

- **`url.PathEscape` on `repoRemoteID`:** GitLab also accepts namespaced paths (`group/project`) as the project identifier in URLs. `url.PathEscape` ensures slashes are percent-encoded (`%2F`) so the URL is routed correctly.

- **`WithHTTPClient` functional option:** Allows tests to inject `httptest.Server`'s client, which is configured to trust the test server's TLS certificate. All 15 unit tests use `httptest.NewServer` with a route map — no real network calls.

- **No retries in the provider layer:** The provider returns errors immediately. Restate (Phase 1.3) is responsible for all retry logic, so adding retries here would interfere with Restate's durability guarantees.

- **Verified against real GitLab:** Integration tests ran against `gitlab.example.com` — project ID 4, MR IID 2 ("Add Bubbletea markdown viewer CLI app"). `ListRepos` returned 7 repos; `GetMRDiff` returned 4 files / 289 changed lines. All 3 integration tests passed.

---

## Subphase 1.3 — Restate Services (Go): DiffFetcher, PostReview, PRReview ✓ Done

**Goal:** Go Restate handlers that orchestrate the review pipeline.

### Tasks

1. **Restate Go SDK setup**
   - `go-services/go.mod` with `github.com/restatedev/sdk-go`
   - `cmd/worker/main.go` — registers services with Restate, listens on configured port

2. **DiffFetcher service**
   - `go-services/internal/difffetcher/service.go`
   - Restate service with handler: `FetchPRDetails(ctx, FetchRequest) -> FetchResponse`
   - `FetchRequest`: repo_id, mr_number, provider_id, encrypted_token, api_url
   - `FetchResponse`: diff (unified diff string), mr_details (title, desc, author, branches, head_sha), changed_files list
   - Decrypts provider token, instantiates GitLab provider, fetches diff + details
   - DB access: reads provider + repo info by ID

3. **PostReview service**
   - `go-services/internal/postreview/service.go`
   - Restate service with handler: `Post(ctx, PostRequest) -> PostResponse`
   - `PostRequest`: review_run_id, repo_remote_id, mr_number, provider config, summary, comments list
   - Posts summary as MR note
   - Posts each inline comment as MR discussion
   - For each posted comment: updates `review_comments` row with `provider_comment_id` and `posted=true`
   - Idempotent: skips comments where `provider_comment_id` is already set (handles retries)

4. **PRReview Virtual Object**
   - `go-services/internal/prreview/service.go`
   - Restate Virtual Object, keyed by `<repo_id>-<mr_number>`
   - `Run` handler (exclusive):
     1. Create `review_runs` row with status=pending
     2. Call `DiffFetcher.FetchPRDetails`
     3. Update status to running
     4. Call `Reviewer.RunReview` (Python service via Restate cross-language call)
     5. Store summary + comments in DB
     6. Call `PostReview.Post`
     7. Update status to completed (or failed on error)
   - Error handling: update review_run status to failed, let Restate handle retries

5. **Worker entrypoint**
   - Register all three services with Restate
   - Database connection pool (pgx)
   - Config from env vars

### Deliverable
- `go-services` binary starts, registers with Restate, and handlers are invocable
- PRReview orchestrates the full pipeline (pending reviewer service in 1.4)

### Implementation notes

**What changed from the plan:**

- **FetchRequest carries only `repo_id` + `mr_number`:** The plan spec had `FetchRequest` include `provider_id`, `encrypted_token`, and `api_url` as fields. Implemented differently: `DiffFetcher` reads provider credentials from the DB itself given only `repo_id`. This keeps encrypted token bytes out of Restate's durable journal (which persists to disk/storage) and removes the need for `PRReview` to load credentials before dispatching to DiffFetcher.

- **Crypto module copied, not shared:** `api-server/internal/crypto/` is copied verbatim into `go-services/internal/crypto/` (85 lines, zero external deps). The alternative — a third `crypto` Go module with a `replace` directive in both consumers — adds Go module machinery for no real benefit at this scale. Both copies are kept in sync manually.

- **Migration 000002 split from 000001:** `summary` on `review_runs` and `provider_comment_id` / `posted` on `review_comments` were deferred from subphase 1.1 and added here as `000002_review_comments_posted.up/down.sql`. Avoids retroactively modifying the 000001 migration which is already applied in development environments.

- **Raw pgx queries, single `queries.go` file:** 7 query functions grouped in `go-services/internal/db/queries.go` using `pgxpool.Pool` directly. No ORM or sqlc — the query surface is small enough that generated code would add noise without value.

- **`newProvider()` and `classifyProviderError()` duplicated across `difffetcher` and `postreview`:** Both are ~10 lines. Extracting them to a shared `internal/providerutil/` package would be a premature abstraction; duplication is the right call at this scale.

- **Restate SDK v0.23.0:** The SDK uses `restate.Reflect(rcvr)` for handler registration. It infers service type from the context type in the method signature: methods with `restate.Context` → plain Service; methods with `restate.ObjectContext` → Virtual Object (exclusive handler). The service name defaults to the struct type name, so naming structs `DiffFetcher`, `PostReview`, `PRReview` directly sets the Restate service names.

- **Cross-service call API:** `restate.Service[O](ctx, "ServiceName", "HandlerName").Request(input)` — output type is a Go generic, input is `any` serialized as JSON. For the cross-language call to the Python Reviewer service, JSON field names must be snake_case and match the Python models exactly.

- **`restate.Context` satisfies `context.Context`:** Confirmed at compile time. `RunContext` (embedded in `restate.Context`) embeds the standard `context.Context` interface, so Restate handler contexts can be passed directly to pgxpool and any other stdlib-context-accepting functions.

- **go.mod upgraded to go 1.24.0:** `go get` upgraded the module directive from 1.23 to 1.24.0 when resolving transitive dependencies.

- **`search-mcp` host port moved to 8081:** Restate's standard ingress port is 8080. `search-mcp` already used 8080 as its host port. Moved `search-mcp` to `host:8081 → container:8080` to avoid the conflict.

- **`expose` (not `ports`) for the worker:** The worker listens on `:9080` for Restate to dispatch handler invocations. This only needs to be reachable within the Docker Compose network (Restate → worker), not from the host. `expose: "9080"` documents the port without publishing it externally.

- **Test scripts in `tests/`:** `tests/unit.sh` (go vet + build + compile-time context assertion + go test -v) and `tests/smoke.sh` (migrate → start stack → register deployment → verify three services appear in Restate admin API).

---

## Subphase 1.4 — Python Reviewer Service ✓ Done

**Goal:** Python Restate handler that takes a diff + MR metadata and returns a structured review via LLM.

### Tasks

1. **Project setup**
   - `reviewer/pyproject.toml` with dependencies: `restate-sdk`, `pydantic-ai[openai]`, `pydantic`
   - Pydantic AI from day 1 — uses its structured output, model abstraction, and agent framework (ready for tools in Phase 2)

2. **Pydantic models**
   - `reviewer/reviewer/models.py`
   - `ReviewRequest`: diff (str), mr_title, mr_description, mr_author, source_branch, target_branch, changed_files (list)
   - `ReviewResponse`: summary (str), comments (list of `ReviewComment`) — used as Pydantic AI `result_type`
   - `ReviewComment`: file_path, line (int), body (str)

3. **Pydantic AI agent**
   - `reviewer/reviewer/agent.py`
   - Create a `pydantic_ai.Agent` with `result_type=ReviewResponse`
   - Model configured via `REVIEW_MODEL` env var (default: `openai:anthropic/claude-sonnet-4-20250514`)
   - Uses Pydantic AI's OpenAI-compatible provider pointed at OpenRouter (`base_url=https://openrouter.ai/api/v1`)
   - System prompt: role as senior code reviewer, output expectations
   - No tools in Phase 1 (search + file reader added in Phase 2)
   - Pydantic AI handles structured output parsing, validation, and retries automatically

4. **Prompt engineering**
   - `reviewer/reviewer/prompt.py`
   - System prompt template: reviewer persona, review guidelines, output format instructions
   - User prompt template: MR metadata (title, description, author, branches) + full diff
   - Pydantic AI enforces the `ReviewResponse` schema — no manual JSON parsing needed

5. **Restate service handler**
   - `reviewer/reviewer/service.py`
   - Restate service: `Reviewer` with handler `RunReview`
   - Receives `ReviewRequest`, builds prompt, runs Pydantic AI agent, returns `ReviewResponse`
   - Runs as HTTP handler for Restate (configurable host/port)

6. **Dockerfile**
   - Python 3.12 slim, install dependencies, run service

### Deliverable
- Reviewer service accepts a diff via Restate, calls LLM, returns structured review with summary + inline comments

### Implementation notes

**What changed from the plan:**

- **`ReviewComment` line range instead of single line:** Plan specified `line` (int) for a single line. Implemented as `line_start` and `line_end` (both int) to support multi-line inline comments. GitLab discussion positions can span ranges, and this lets the reviewer mark entire code blocks (e.g., a multi-statement conditional) as problematic. If a single line is affected, both fields get the same value.

- **Pydantic AI `instructions` instead of `system_prompt`:** The plan referenced a `system_prompt` parameter. Pydantic AI's API uses `instructions` on the `Agent` constructor for the same purpose. The prompt content matches the plan's intent — reviewer persona, guidelines focused on bugs/security/correctness (not style), diff line numbering rules, and structured output expectations.

- **`REVIEW_MODEL` default without `openai:` prefix:** Plan specified default `openai:anthropic/claude-sonnet-4-20250514`. Since the agent is constructed with an explicit `OpenAIChatModel` + `OpenAIProvider`, the model name is passed directly to the provider as `anthropic/claude-sonnet-4-20250514` (the OpenRouter model identifier) — no `openai:` prefix needed.

- **Hypercorn as ASGI server:** The plan mentioned "runs as HTTP handler for Restate (configurable host/port)" without specifying a server. Chose Hypercorn (`hypercorn>=0.17.0`) as the ASGI server for the Restate SDK's async app. Configured via `REVIEWER_HOST` and `REVIEWER_PORT` env vars (defaults `0.0.0.0:9090`).

- **Restate server upgraded from 1.1 to 1.6:** The Docker Compose stack originally used `restatedev/restate:1.1`, which is incompatible with the Go SDK v0.23.0 (requires Restate 1.3+). Discovery requests to the worker failed with HTTP 415 (Unsupported Media Type). Updated to `restatedev/restate:1.6`. Also fixed the smoke test's service verification query — Restate 1.6 wraps `/services` response in `{"services": [...]}` instead of a bare array.

- **No deviations on scope:** No tools, no search integration, no file reader — correctly deferred to Phase 2 as planned.

---

## Subphase 1.5 — API Server (ConnectRPC) ✓ Done

**Goal:** Go HTTP server exposing the admin API via ConnectRPC.

### Tasks

1. **Project setup**
   - `api-server/go.mod` with connect-go, pgx, golang-migrate
   - `cmd/server/main.go` — run migrations on startup, start HTTP server

2. **Database layer**
   - `api-server/internal/db/` — queries using sqlc or hand-written with pgx
   - Queries: CRUD providers, list repos, enable/disable review, create/get review runs + comments
   - Encrypt token on provider create, decrypt on read

3. **Provider handler**
   - `CreateProvider` — validate input, encrypt token, insert into DB, trigger `ProviderSync` (for Phase 1: directly call GitLab `ListRepos` and upsert repos into DB — no Restate service needed for sync, keep it simple)
   - `ListProviders` — return providers (tokens redacted)
   - `DeleteProvider` — soft or hard delete

4. **Repo handler**
   - `ListRepos` — list repos for a provider (from DB)
   - `EnableReview` / `DisableReview` — toggle `review_enabled`

5. **Review handler**
   - `TriggerReview` — validate repo is enabled, call Restate ingress to submit `PRReview.Run` invocation
   - `GetReviewRun` — return review run with comments from DB

6. **Restate ingress client**
   - `api-server/internal/restate/client.go`
   - HTTP client that calls Restate ingress API to submit invocations
   - `SubmitPRReview(repoID, mrNumber) -> invocationID`

7. **Config & startup**
   - Env vars: `DATABASE_URL`, `ENCRYPTION_KEY`, `RESTATE_INGRESS_URL`, `LISTEN_ADDR`
   - Run migrations, initialize DB pool, register ConnectRPC handlers, start server

### Deliverable
- API server starts, runs migrations, exposes ConnectRPC endpoints
- `TriggerReview` kicks off the full pipeline via Restate

### Implementation notes

**What changed from the plan:**

- **Hand-written pgx queries (no sqlc):** The plan offered "sqlc or hand-written pgx". Went with hand-written for consistency with `go-services/internal/db/` — the query surface is small enough that sqlc's generated types would add more machinery than value.

- **`CreateProvider` fetches repos before writing to DB (atomic transaction):** A bug discovered during testing: the plan implied insert-then-sync, but if `ListRepos` fails after the provider is already inserted, an orphaned provider row is left in the DB. Fixed by calling `ListRepos` first, then wrapping the `INSERT INTO providers` + repo upserts in a single transaction. If the GitLab call fails, the database is untouched.

- **Provider soft-delete, not hard-delete:** The plan said "soft or hard delete". Implemented as soft-delete: a `deleted_at TIMESTAMPTZ` column added via migration `000003_provider_soft_delete`. All list/get queries filter `WHERE deleted_at IS NULL`. Hard-deleting providers would cascade to repositories and review history, losing audit data. The `repositories.provider_id` FK is also changed from `ON DELETE CASCADE` to `ON DELETE RESTRICT` — since providers are never hard-deleted, cascade would only fire on accidental raw SQL `DELETE`.

- **`RunID` ownership at the API server, not the worker:** The plan had `TriggerReview` call Restate and return. Implemented so the API server creates the `review_run` row (gets the UUID immediately), passes `run_id` in the fire-and-forget payload to Restate, and the `PRReview.Run` worker uses it directly if present (skipping its own `CreateReviewRun` call). This means:
  - The caller gets a valid run ID in the `TriggerReview` response without waiting for Restate to start processing.
  - The `review_run` status is immediately queryable as `pending`.
  - The worker's `RunID != ""` check is a one-line guard — no interface change, fully backward-compatible (webhooks in Phase 2 can still call the worker without a pre-created run ID).

- **Fire-and-forget via Restate `/send` endpoint:** `SendPRReview` posts to `POST {baseURL}/PRReview/{key}/Run/send` (not the synchronous invoke endpoint). Restate returns `202 Accepted` immediately. The API response is returned to the caller as soon as the run is created and the message is accepted — no waiting for the review to complete.

- **`h2c` wrapper for gRPC + Connect compatibility:** The HTTP mux is wrapped with `h2c.NewHandler` (HTTP/2 cleartext). This lets both gRPC clients (which require HTTP/2) and Connect JSON clients (HTTP/1.1) use the same port without TLS, which is correct for a service that sits behind a load balancer or is called from `curl`.

- **Migrations embedded in binary:** `migrations/embed.go` uses `//go:embed *.sql` to embed all SQL files into the api-server binary. On startup, `main.go` calls `migrate.Up()` programmatically via `iofs` source + `pgx/v5` driver. This replaces the `migrate` Compose profile service from subphase 1.1 — the api-server now manages its own schema, and there's no need for a separate migration container or external file mount.

- **Provider package copied into api-server:** `api-server/internal/provider/` is a copy of `go-services/internal/provider/` with the import path updated to `ai-reviewer/api-server/internal/provider`. This follows the same pattern as `crypto/` (already duplicated between the two modules). The api-server only needs `ListRepos` for the provider sync on `CreateProvider`, but keeping the full interface avoids drift if more endpoints are needed later.

- **`go-services/Dockerfile` updated to distroless:** Switched the worker's runtime image from `alpine:3.20` to `gcr.io/distroless/static-debian12` for a smaller attack surface and consistent base with the api-server image. The api-server Dockerfile uses distroless from the start.

- **`docker-compose.yml` `migrate` profile now superseded:** The `api-server` service runs migrations on startup, so the standalone `migrate` Compose profile service (added in 1.1) is effectively unused for normal operation. It's kept for manual one-off migration runs but is no longer part of the `docker compose up` flow.

---

## Subphase 1.6 — Docker Compose & Integration ✓ Done

**Goal:** Full stack running with `docker compose up`. End-to-end manual test.

### Tasks

1. **Update `docker-compose.yml`**
   - Services: `postgres`, `restate`, `api-server`, `go-services`, `reviewer`
   - Keep existing `qdrant`, `search-mcp`, `indexer` (but not in default profile)
   - Dependency ordering: postgres → api-server (migrations) → restate → go-services + reviewer
   - Restate admin API exposed for UI access

2. **Update `.env.example`**
   - Add: `DATABASE_URL`, `ENCRYPTION_KEY`, `RESTATE_INGRESS_URL`, `REVIEW_MODEL`, `OPENROUTER_API_KEY`

3. **Restate service registration**
   - go-services and reviewer register their endpoints with Restate on startup
   - Docker compose health checks to ensure ordering

4. **End-to-end test script**
   - `scripts/test-e2e.sh` (or documented curl commands):
     1. Create a provider (GitLab API URL + token)
     2. List repos → verify synced
     3. Enable review for a repo
     4. Trigger review for a specific MR
     5. Poll review run status until completed
     6. Verify comments posted on GitLab MR

5. **Restate UI access**
   - Expose Restate admin UI port in docker-compose
   - Document how to access it

### Deliverable
- `docker compose up` starts the full stack
- Admin can register a GitLab provider, enable review, trigger a review, and see comments on the MR
- Restate UI accessible for debugging

### Implementation notes

**What changed from the plan:**

- **`restate-register` init container instead of in-process registration:** The plan said "go-services and reviewer register their endpoints with Restate on startup". Restate's design requires the orchestrator (Restate) to call out to the service to discover its handlers via `POST /deployments` — services don't connect to Restate themselves. Implemented as a one-shot `curlimages/curl` container (`restate-register`) that runs after Restate is healthy and both worker/reviewer have started, retrying until both deployments register successfully. This is idempotent — re-running `docker compose up` re-registers without side effects.

- **Restate healthcheck uses `/bin/curl` from the Restate image:** The plan noted to fall back to `grpcurl` or TCP if `curl` wasn't present. Verified that the `restatedev/restate:1.6` image (Debian-based) includes `curl` at `/bin/curl`. Used `CMD curl -sf http://localhost:9070/health` directly.

- **`curlimages/curl` for the init container:** Chosen over `alpine` + manual curl install or a full `ubuntu` image — it's a minimal (~5 MB) image purpose-built for curl invocations with no shell overhead beyond `ash`.

- **`$$VAR` escaping in Docker Compose inline shell commands:** Docker Compose interpolates `$VAR` in YAML values before passing to the container. Used `$$VAR` to produce literal `$VAR` in the shell script (e.g., `$$RESTATE_ADMIN` → `$RESTATE_ADMIN`, `$$i` → `$i`). The `$(seq 1 30)` subshell syntax does not need escaping as Compose only interpolates `$IDENTIFIER` patterns.

- **`worker` and `reviewer` upgraded to `restate: service_healthy`:** Previously both used `condition: service_started` (Restate up but not necessarily accepting requests). Upgraded to `service_healthy` so Go/Python services don't start (and potentially fail their Restate connection) before Restate's admin API is ready.

- **`api-server` added `restate: service_healthy` dependency:** The plan had `api-server` depend only on `postgres`. Added `restate: service_healthy` to prevent the API server from accepting `TriggerReview` calls before Restate is ready to receive fire-and-forget submissions.

- **`smoke.sh` rewritten to use `restate-register` status polling:** Removed the manual registration loop. The smoke test now starts `restate-register` as part of `docker compose up -d` and polls `docker compose ps --format json restate-register` until it exits 0. Registration verification (listing services via admin API) is unchanged.

- **`tests/e2e.sh` placed in `tests/` (not `scripts/`):** The plan mentioned `scripts/test-e2e.sh`. Placed alongside `smoke.sh` in `tests/` for consistency.

- **`make unit` uses per-module `go test`:** `make unit` runs `go test ./...` separately in `go-services/` and `api-server/` (both have their own `go.mod`). Running from the repo root would fail since there's no root-level `go.mod`.

- **`.env.example` sectioned with comments:** Added grouping headers (Embedding/Indexer, Database, Security, Restate, LLM) and the previously missing `RESTATE_INGRESS_URL=http://localhost:8080` with an explanatory comment.

---

## Definition of Done (Phase 1 complete)

1. `docker compose up` starts all services with a clean PostgreSQL database (migrations applied on startup)
2. Admin can register a self-hosted GitLab provider via API providing `api_url` + `token`, and the system syncs the list of repos from GitLab
3. Admin can enable review for a specific repo via API
4. Admin can trigger a review for a specific MR via API (providing repo + MR number)
5. The reviewer produces a summary comment and inline comments posted directly to the MR in GitLab
6. A service crash mid-review is automatically retried by Restate without duplicate comments
7. Review run and comments are persisted in PostgreSQL
8. Restate UI is accessible and shows invocation history

## Dependencies & Key Libraries

| Component | Key Dependencies |
|---|---|
| api-server | connect-go, pgx, golang-migrate, buf (codegen) |
| go-services | restate-sdk-go, pgx, gitlab API client (hand-rolled or go-gitlab) |
| reviewer | restate-sdk (Python), pydantic-ai[openai], pydantic |
| infra | PostgreSQL 16, Restate Server, Docker Compose |
