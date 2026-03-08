# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`ai-reviewer` is a self-hosted AI-powered PR review system that posts summary and inline review comments on merge requests. It uses Restate for durable workflow orchestration, Pydantic AI for LLM-based review, and Qdrant for semantic code search.

**Current status:** Phase 1 (MVP) and Phase 2 (Semantic Search & Context-Aware Review) complete. Work is now tracked via the task management system in `tasks/`. See `specs/phases.md` for historical roadmap and `specs/later.md` for known issues.

Full technical design: `specs/overview.md`

## Extra instructions
- always use gopls lsp plugin while working with golang code
- always build binaries into `out` folder that is put to gitignore
- before starting work, check that `.env` exists and has `ENCRYPTION_KEY` set to a non-empty value; if not, generate one with `python3 -c "import secrets; print(secrets.token_hex(32))"` and set it

## Components

Each component has its own `CLAUDE.md` with detailed architecture and commands:

| Component | Description | CLAUDE.md |
|---|---|---|
| [api-server](api-server/) | Go ConnectRPC HTTP server — admin API, migrations, Restate ingress client | [api-server/CLAUDE.md](api-server/CLAUDE.md) |
| [go-services](go-services/) | Go Restate handlers — DiffFetcher, PostReview, PRReview orchestrator, RepoSyncer | [go-services/CLAUDE.md](go-services/CLAUDE.md) |
| [reviewer](reviewer/) | Python Restate service — Pydantic AI agent with tools (search + file reader), LLM-based code review | [reviewer/CLAUDE.md](reviewer/CLAUDE.md) |
| [indexer](indexer/) | Python Restate service — indexes Git repos (bare clones) into Qdrant with tree-sitter chunking | [indexer/CLAUDE.md](indexer/CLAUDE.md) |
| [search-mcp](search-mcp/) | Python FastMCP server — semantic code search over Qdrant | [search-mcp/CLAUDE.md](search-mcp/CLAUDE.md) |
| [proto](proto/) | Protobuf API definitions (provider, repo, review services) | — |
| [gen](gen/) | Generated Go code from protobuf (shared module) | — |
| [e2e](e2e/) | Go e2e test suite — full-stack tests with mock GitLab + LLM servers | [e2e/CLAUDE.md](e2e/CLAUDE.md) |

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

# Run e2e tests (mock mode — no external services needed, requires Docker)
# Rebuilds containers first; output is tee'd to a unique /tmp/e2e-test-XXXXXX.log for post-failure inspection
make e2e

# Run e2e tests in live mode (requires a real GitLab instance)
E2E_LIVE=1 \
GITLAB_URL=https://gitlab.example.com \
GITLAB_TOKEN=glpat-... \
GITLAB_MR_IID=42 \
GITLAB_PROJECT_REMOTE_ID=123 \
make e2e

# Note: gen/go/ is gitignored — run `make proto` before running e2e tests

# Run unit tests (Go: go test in api-server + go-services)
make unit

# Run full unit test suite with vet, build, and compile checks (go-services only)
./tests/unit.sh

# Generate protobuf code (gen/go/ is gitignored — must run before first build)
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
# Expected: DiffFetcher, PostReview, PRReview, RepoSyncer, Reviewer, Indexer, IndexMainBranch
```

## Architecture

### Service Topology

```
GitLab Webhook ──┐
                 ▼
Admin API ──→ API Server (:8090 host, ConnectRPC)
                    │
                    ├── PostgreSQL (:5432)
                    │
                    └── Restate (:8080 ingress, :9070 admin, :9071 UI)
                            │
                            ├── Go Worker (:9080) — DiffFetcher, PostReview, PRReview, RepoSyncer
                            ├── Python Reviewer (:9090) — Reviewer (with search + file reader tools)
                            └── Python Indexer (:9091) — Indexer
                                                            Qdrant (:6333 REST, :6334 gRPC)
                                                                ▲
                                                        Search-MCP (:8081 host)
```

### Ports

| Service | Container port | Host port |
|---|---|---|
| API Server | 8090 | 8090 |
| Restate ingress | 8080 | 8080 |
| Restate admin | 9070 | 9070 |
| Restate UI | 9071 | 9071 |
| PostgreSQL | 5432 | 5432 |
| Qdrant REST | 6333 | 6333 |
| Qdrant gRPC | 6334 | 6334 |
| Search-MCP | 8080 | 8081 |
| Worker | 9080 | (internal only) |
| Reviewer | 9090 | (internal only) |
| Indexer | 9091 | (internal only) |

### Infrastructure

- **PostgreSQL** — providers, repos, review runs, review comments. Migrations managed by golang-migrate (embedded in api-server binary).
- **Restate** — durable workflow orchestration. Virtual Object `PRReview` ensures one review per MR at a time.
- **Qdrant** — vector database on ports `6333` (REST) and `6334` (gRPC), with data persisted in `./db`. One collection per repo+branch, named `<repo_id>_<branch>` via `sanitize_collection_name`.
- **Repos volume** — Docker volume mounted at `/data/repos` in worker, reviewer, and indexer containers. Stores bare git clones managed by `RepoSyncer`. One bare clone per repo at `/data/repos/<repo_id>/`.
- `MODEL_DIMENSIONS` dict is duplicated in both `indexer/indexing.py` and `search-mcp/server.py` — keep them in sync.

### Go Multi-Module Setup

Three Go modules: `api-server/`, `go-services/`, `gen/go/`, linked by a `go.work` workspace at the root. Both `api-server` and `go-services` import `gen/go` via a `replace` directive. The `crypto/` and `provider/` packages are duplicated between `api-server` and `go-services` — keep them in sync.

`gen/go/` is gitignored — run `make proto` to generate it before building.

### Embedding

OpenAI-compatible models (`text-embedding-3-small` / `text-embedding-3-large`) called via OpenRouter (`https://openrouter.ai/api/v1`).

## Task Workflow

This project uses a file-based task management system. All work is organized as task files (markdown with YAML frontmatter). The `TASKS_DIR` environment variable must be set to point to the tasks directory.

Use the slash commands for agent workflows:
- `/plan [task-id|next]` — find a `todo` task, write an implementation plan, mark it `ready`
- `/implement [task-id|next]` — find a `ready` task, create a git worktree at `../<task-id>`, execute the plan there, mark it `in_review`

### CLI Reference

```bash
task create "Name" [--type bug|feature|refactor] [--tags t1,t2] [--parent <id>]
task list [--status STATUS] [--parent ID] [--blocked] [--assigned-to NAME] [--tag TAG]
task show <id>
task update <id> [--status STATUS] [--assigned-to NAME] [--blocked-by ID] [--remove-blocked-by ID]
task next [--assigned-to NAME]
task log <id> "message" [--section findings|changes|decisions]
task log-show <id>
task templates
```

Tasks, logs, and config are stored in `$TASKS_DIR/`.
