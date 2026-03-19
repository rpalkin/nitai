# Data Model

Core entities and database schema for ai-reviewer.

## Key Entities

| Entity | Key Fields |
|---|---|
| Organization | id, name, created_at |
| User | id, org_id, email, password_hash, created_at |
| Provider | id, org_id, type (github/gitlab/gitlab_self_hosted), api_url, token (encrypted), webhook_secret |
| Repository | id, org_id, provider_id, remote_id, name, full_name, default_branch, review_enabled |
| BranchIndex | id, repo_id, branch, last_indexed_commit, collection_name, updated_at |
| ReviewInstruction | id, org_id, name, content, repo_filter (UUID[]), file_pattern_filter (TEXT[]), enabled |
| PRReview | id, repo_id, pr_number, diff_hash, status (pending/completed/failed/skipped/draft), summary, created_at, is_dry_run |
| ReviewComment | id, review_id, file_path, line, body, provider_comment_id, posted (bool) |
| ReviewFeedback | id, comment_id, user_identifier, rating (positive/negative), created_at |
| ActivityLog | id, org_id, repo_id, action, details, created_at |

## PostgreSQL

- **Stores:** organizations, users, providers (credentials encrypted at rest via AES-256-GCM, key from `ENCRYPTION_KEY` env var), repos, review runs, inline comments (including body), activity logs, custom instructions with filter rules, review feedback
- **PR tracking:** stores diff hash per PR to detect re-reviews vs. new changes
- **Review comments:** full comment body stored alongside metadata (file, line, provider comment ID). This enables analytics, debugging, and resilience against comments deleted on the provider side.

## Schema Migrations

Database schema is managed with **golang-migrate**. Migration files live in `migrations/` using the sequential naming convention (`000001_init.up.sql` / `000001_init.down.sql`).

- **On startup:** The API Server runs `migrate.Up()` automatically before accepting traffic. This ensures the schema is always current after an upgrade.
- **CLI:** `migrate` CLI is available in the Docker image for manual operations (`migrate -path migrations -database $DATABASE_URL up/down/version`).
- **CI:** A test step verifies that migrations apply cleanly to an empty database and that each `down` migration cleanly reverses its corresponding `up`.
- **Versioning:** Each migration is idempotent where possible (`CREATE TABLE IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS`). Destructive changes (column drops, type changes) get their own migration with a clear description.

## Key Decisions

- **Provider tokens encrypted at rest (AES-256-GCM)** — key from `ENCRYPTION_KEY` env var
- **golang-migrate with auto-migration on startup** — schema always current after upgrade
- **`posted` flag + `provider_comment_id` for idempotency** — prevents duplicate comment posting on retry