# Deployment

Self-hosted deployment architecture and operational configuration.

## Service Topology

```
GitLab Webhook ~-+
                  v
Admin API ~-> API Server (:8090 host, ConnectRPC)
                    |
                    +~- PostgreSQL (:5432)
                    |
                    v Restate (:8080 ingress,:9070 admin,:9071 UI)
                            |
                            +~- Go Worker (:9080) — DiffFetcher, PostReview, PRReview, RepoSyncer
                            +~- Python Reviewer (:9090) — Reviewer (with search + file reader tools)
                            v~- Python Indexer (:9091) — Indexer
                                                            Qdrant (:6333 REST, :6334 gRPC)
                                                                ~^
                                                        Search-MCP (:8081 host)
```

### Service Types

| Service | Type | Language | Handlers |
|---|---|---|---|
| `PRReview` | Virtual Object | Go | Run (exclusive) |
| `ProviderSync` | Service | Go | Sync |
| `DiffFetcher` | Service | Go | FetchPRDetails, CheckPreviousReview, FetchRepoRules |
| `RepoSyncer` | Service | Go | SyncRepo |
| `PostReview` | Service | Go | Post |
| `Indexer` | Service | Python | IndexRepo |
| `Reviewer` | Service | Python | RunReview (Pydantic AI + Search-MCP + file reader) |

## Ports

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

## Infrastructure

- **PostgreSQL** — providers, repos, review runs, review comments. Migrations managed by golang-migrate (embedded in api-server binary).
- **Restate** — durable workflow orchestration. Virtual Object `PRReview` ensures one review per MR at a time.
- **Qdrant** — vector database on ports `6333` (REST) and `6334` (gRPC), with data persisted in `./db`. One collection per repo+branch, named `<repo_id>_<branch>` via `sanitize_collection_name`.
- **Repos volume** — Docker volume mounted at `/data/repos` in worker, reviewer, and indexer containers. Stores bare git clones managed by `RepoSyncer`. One bare clone per repo at `/data/repos/<repo_id>/`.
- **FastEmbed cache** — Bind mount at `./data/fastembed-cache:/data/fastembed-cache` shared by indexer and search-mcp containers. Caches ONNX models downloaded by fastembed (used for sparse vectors in hybrid search). The `FASTEMBED_CACHE_PATH` env var points fastembed to this location. Uses a bind mount instead of a named volume so the cache survives `docker compose down -v`.

## Restate

- **Purpose:** Durable execution engine — orchestrates multi-step, retryable workflows with built-in state management
- **Deployment:** Single Rust binary with embedded RocksDB storage. No external database required.
- **Key services:**
  - **PRReview** (Virtual Object) — the main review pipeline, keyed by `<repo_id>-<pr_number>` (see specs/review-pipeline/)
  - **ProviderSync** — triggered when a provider is added/updated; lists repos via provider API, upserts to DB
  - **IndexMainBranch** (Virtual Object) — periodic background indexing of primary branches, keyed by `<repo_id>` (see specs/indexing/)
- **Concurrency control:** `PRReview` is a Virtual Object with an exclusive `Run` handler. Only one review can execute per PR at a time. When a new push arrives, the webhook handler cancels the in-flight invocation via Restate's admin API (`PATCH /invocations/PRReview/<key>/Run/cancel`) and submits a fresh one. This prevents stale reviews from being posted.
- **UI:** Restate ships with a built-in web UI for inspecting running invocations, viewing execution timelines, debugging failures, and restarting failed invocations.

## Restate Service Registration

Restate requires all worker/reviewer HTTP deployments to be **registered** before it can route workflow invocations. Registration is handled automatically by the `restate-register` init container, which runs once on `docker compose up` and exits. It waits for Restate to be healthy, then POSTs to `/deployments` for both the `worker` (`:9080`) and `reviewer` (`:9090`) services.

Re-registration is idempotent — running `docker compose up` again is safe.

**Restate UI:** `http://localhost:9071` — shows invocation history, workflow state, and registered services.

To verify registered services manually:
```bash
curl http://localhost:9070/services | jq '.services[].name'
# Expected: DiffFetcher, PostReview, PRReview, RepoSyncer, Reviewer, Indexer, IndexMainBranch
```

## Docker Compose

Self-hosted. All components run on the customer's infrastructure. A `docker-compose.yml` defines:
- API Server
- Go Service (registers all Go handlers with Restate)
- Python Indexer Service
- Python Reviewer Service
- Restate Server (single binary, embedded storage)
- PostgreSQL
- Qdrant

Configuration via environment variables / `.env` file.

## Observability

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

## Key Decisions

- **Single Restate binary with embedded RocksDB** — simplifies deployment, no external database
- **Restate registration via init container** — automatic on `docker compose up`
- **Docker volume at `/data/repos` shared across worker/reviewer/indexer** — bare git clones accessible to all services
- **OpenRouter as sole LLM gateway** — single point for rate limiting and cost tracking