# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`ai-reviewer` is a self-hosted AI-powered PR review system that posts summary and inline review comments on merge requests. It uses Restate for durable workflow orchestration, Pydantic AI for LLM-based review, and Qdrant for semantic code search.

**Current status:** Phase 1 (MVP) complete — diff-only GitLab reviewer with manual API trigger. See `specs/phases.md` for the full roadmap and `specs/later.md` for known issues and deferred items.

Full technical design: `specs/overview.md`

## Extra instructions
- always use gopls lsp plugin while working with golang code

## Completing a subphase

When a subphase is implemented, do the following in order:

1. **Update `specs/phases.md`** — set Phase N status to `In progress` (if not already), add the subphase to the `Completed` list. Keep notes brief; details belong in the phase plan file.
2. **Update `specs/phaseN-plan.md`** — mark the subphase heading `✓ Done`, add an `### Implementation notes` section following the style in `phase1-plan.md`: use `**What changed from the plan:**` for deviations and `**Architectural decisions:**` for design choices made during implementation.

## Components

Each component has its own `CLAUDE.md` with detailed architecture and commands:

| Component | Description | CLAUDE.md |
|---|---|---|
| [api-server](api-server/) | Go ConnectRPC HTTP server — admin API, migrations, Restate ingress client | [api-server/CLAUDE.md](api-server/CLAUDE.md) |
| [go-services](go-services/) | Go Restate handlers — DiffFetcher, PostReview, PRReview orchestrator | [go-services/CLAUDE.md](go-services/CLAUDE.md) |
| [reviewer](reviewer/) | Python Restate service — Pydantic AI agent, LLM-based code review | [reviewer/CLAUDE.md](reviewer/CLAUDE.md) |
| [indexer](indexer/) | Python CLI — indexes Git repos into Qdrant with tree-sitter chunking | [indexer/CLAUDE.md](indexer/CLAUDE.md) |
| [search-mcp](search-mcp/) | Python FastMCP server — semantic code search over Qdrant | [search-mcp/CLAUDE.md](search-mcp/CLAUDE.md) |
| [proto](proto/) | Protobuf API definitions (provider, repo, review services) | — |
| [gen](gen/) | Generated Go code from protobuf (shared module) | — |

## Commands

Requires Docker and Docker Compose. Copy `.env.example` to `.env` and fill in the required variables.

```bash
# Start the full stack (builds images, runs migrations, registers Restate services automatically)
make up
# or: docker compose up -d --build

# Tear down
make down
# or: docker compose down

# Tail all logs
make logs

# Run smoke tests (verifies Restate registration, service health)
make smoke
# or: ./tests/smoke.sh [--no-teardown]

# Run end-to-end tests (requires GitLab env vars — skipped if not set)
GITLAB_URL=https://gitlab.example.com \
GITLAB_TOKEN=glpat-... \
GITLAB_MR_IID=42 \
GITLAB_PROJECT_REMOTE_ID=123 \
make e2e

# Run unit tests (Go modules + Python)
make unit

# Generate protobuf code
make proto

# Index a repository (one-shot, runs and exits)
REPO_PATH=/path/to/repo docker compose run --rm indexer

# Pass extra flags to the indexer
REPO_PATH=/path/to/repo docker compose run --rm indexer /repo --recreate

# Install Python modules locally (for linting / IDE support)
pip install -e indexer/
pip install -e search-mcp/
pip install -e reviewer/

# Test search via CLI client (against search-mcp on :8081)
python search-mcp/client.py list
python search-mcp/client.py search <collection> "query text" [--top-k 5]
```

### Restate service registration

Restate requires all worker/reviewer HTTP deployments to be **registered** before it can route workflow invocations. Registration is handled automatically by the `restate-register` init container, which runs once on `docker compose up` and exits. It waits for Restate to be healthy, then POSTs to `/deployments` for both the `worker` (`:9080`) and `reviewer` (`:9090`) services.

Re-registration is idempotent — running `docker compose up` again is safe.

**Restate UI:** `http://localhost:9071` — shows invocation history, workflow state, and registered services.

To verify registered services manually:
```bash
curl http://localhost:9070/services | jq '.services[].name'
# Expected: DiffFetcher, PostReview, PRReview, Reviewer
```

## Architecture

### Service Topology

```
Admin API (curl/Postman) → API Server (:8080, ConnectRPC)
                                │
                                ├── PostgreSQL (:5432)
                                │
                                └── Restate (:8080 ingress, :9070 admin, :9071 UI)
                                        │
                                        ├── Go Worker (:9080) — DiffFetcher, PostReview, PRReview
                                        └── Python Reviewer (:9090) — Reviewer
```

### Infrastructure

- **PostgreSQL** — providers, repos, review runs, review comments. Migrations managed by golang-migrate (embedded in api-server binary).
- **Restate** — durable workflow orchestration. Virtual Object `PRReview` ensures one review per MR at a time.
- **Qdrant** — vector database on ports `6333` (REST) and `6334` (gRPC), with data persisted in `./db`. One collection per repository, named from git remote URL via `sanitize_collection_name`.
- `MODEL_DIMENSIONS` dict is duplicated in both `indexer/main.py` and `search-mcp/server.py` — keep them in sync.

### Go Multi-Module Setup

Three Go modules: `api-server/`, `go-services/`, `gen/go/`. Both `api-server` and `go-services` import `gen/go` via a `replace` directive. The `crypto/` and `provider/` packages are duplicated between `api-server` and `go-services` — keep them in sync.

### Embedding

OpenAI-compatible models (`text-embedding-3-small` / `text-embedding-3-large`) called via OpenRouter (`https://openrouter.ai/api/v1`).
