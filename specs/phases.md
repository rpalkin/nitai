# AI Reviewer — Implementation Phases

## Phase 1 — MVP: Diff-Only GitLab Reviewer ✓ Done

- **Status:** Done
- **Completed:** All subphases (1.1–1.6)
- **Notes:** Full stack assembles with `docker compose up --build`. Restate service registration is now automatic via `restate-register` init container. Smoke and e2e test scripts in `tests/`. See `phase1-plan.md` for detailed implementation notes and deviations from the plan. Known issues and deferred items tracked in `later.md`.

**Scope:** End-to-end review loop — manual trigger in, comments out — for self-hosted GitLab. Restate for durable orchestration from the start. No webhook, no semantic search, no repo cloning, no admin UI.

**What gets built:**
- PostgreSQL schema (providers, repos, review runs, review comments) — managed with golang-migrate
- Go API server with: minimal admin API (CRUD provider, list/enable repos, trigger review for a specific MR)
- Restate server for durable workflow orchestration
- `PRReview` Virtual Object with exclusive `Run` handler orchestrating the pipeline
- `DiffFetcher` Go service — `FetchPRDetails` handler (fetches diff + metadata from GitLab)
- `PostReview` Go service — posts summary comment + inline comments to GitLab MR
- GitLab provider implementation (subset of `GitProvider` interface: `ListRepos`, `GetPRDiff`, `GetPRDetails`, `PostComment`, `PostInlineComment`)
- Python reviewer service as Restate handler — receives diff + MR metadata, calls LLM via OpenRouter, returns structured output (summary + inline comments). No tools (no search, no file reader).
- Docker Compose for the full stack (API server, Go service, reviewer, Restate, PostgreSQL)

**Not included:** Webhook, debounce, cancel-on-new-push, Qdrant, indexer, search-MCP, repo syncer, admin UI, GitHub support, custom instructions, re-review dedup (diff-hash check), draft MR handling, reconciliation.

**Definition of Done:**
1. `docker compose up` starts all services (including Restate) with a clean PostgreSQL database (migrations applied on startup)
2. Admin can register a self-hosted GitLab provider via API (curl/Postman) providing `api_url` + `token`, and the system syncs the list of repos from GitLab
3. Admin can enable review for a specific repo via API
4. Admin can trigger a review for a specific MR via API (providing repo + MR number)
5. The reviewer produces a summary comment and inline comments posted directly to the MR in GitLab
6. A service crash mid-review is automatically retried by Restate without duplicate comments
7. Review run and comments are persisted in PostgreSQL
8. Restate UI is accessible and shows invocation history

---

## Phase 2 — Semantic Search & Context-Aware Review

- **Status:** In progress
- **Completed:** 2.1 (Webhook Endpoint & Signature Validation), 2.2 (Webhook → Restate Dispatch), 2.3 (Debounce & Diff-Hash Dedup), 2.4 (Draft MR Tracking), 2.5 (Repo Syncer Service)
- **Notes:** See `phase2-plan.md` for detailed implementation notes and decisions.

**Scope:** Webhook-driven automation, semantic search, and context-aware review. Add diff-hash dedup and draft MR handling.

**What gets built:**
- Webhook endpoint for GitLab MR events (open/update/synchronize, marked ready)
- Webhook handler does cancel + submit via Restate ingress (cancel-on-new-push)
- 3-minute debounce via `ctx.sleep`
- Webhook signature validation for GitLab
- `DiffFetcher.CheckPreviousReview` handler — diff-hash dedup (skip review if unchanged)
- Draft MR handling (track status, skip review; marking ready triggers review)
- Repo syncer service — bare git clone management on local disk
- Indexer adapted as Restate Python service (existing module)
- Qdrant added to Docker Compose
- Search-MCP integrated as tool for the reviewer
- File reader tool (git show on bare clone at pinned SHA)
- `BranchIndex` tracking in PostgreSQL
- Background indexing (`IndexMainBranch` Virtual Object) on push to primary branch + self-scheduled periodic runs

**Definition of Done:**
1. Configuring the webhook URL in GitLab and opening/updating an MR triggers the review pipeline automatically
2. Rapid sequential pushes to an MR result in exactly one review (debounce + cancel-on-new-push)
3. Webhook signatures are validated; invalid payloads are rejected
4. Reopening an MR with no code changes skips the review (diff-hash match)
5. Draft MRs are tracked but not reviewed; marking ready triggers review
6. Enabling review for a repo triggers initial indexing of the primary branch
7. The reviewer can search the codebase and read files outside the diff during review
8. Review quality demonstrably improves on PRs that depend on code not in the diff (e.g., calling a function defined elsewhere)
9. Re-indexing is incremental (only changed files)
10. Push to main branch triggers background re-indexing

---

## Phase 3 — Admin Console & Custom Instructions

- **Status:** Not started
- **Completed:** —
- **Notes:** —

**Scope:** Web UI for managing the system. Custom review instructions with filtering.

**What gets built:**
- React/TypeScript/Vite admin console
- ConnectRPC communication with API server
- Auth (email + password, JWT sessions)
- Organization management
- Provider CRUD (with webhook URL + secret display)
- Repo listing, enable/disable review
- Custom instructions with filter rules (repos, file patterns, languages)
- `.review-rules.yaml` support (ignore globs + repo-level instructions)
- Activity log viewer
- Manual review trigger (live + dry-run)

**Definition of Done:**
1. Admin can register, log in, and manage providers/repos entirely through the web UI
2. Custom instructions are injected into the reviewer prompt and respect filter rules
3. `.review-rules.yaml` ignore globs exclude files from the diff before review
4. Dry-run review shows results in the UI without posting to GitLab
5. Activity log shows a timeline of all review events per repo

---

## Phase 4 — Re-review & Comment Lifecycle

- **Status:** Not started
- **Completed:** —
- **Notes:** —

**Scope:** Intelligent handling of subsequent reviews on the same MR.

**What gets built:**
- Previous review comments loaded and passed to reviewer
- LLM output supports update action referencing previous comment IDs
- `PostReview` updates existing comments when instructed
- Summary comment posted only on first review; subsequent reviews add only inline comments
- `.review-rules.yaml` modification detection + warning comment

**Definition of Done:**
1. Pushing new commits to a previously-reviewed MR produces a review that doesn't repeat already-posted comments
2. If an issue is fixed, the reviewer can update the old comment acknowledging resolution
3. Summary comment is posted once; follow-up reviews add only new inline findings
4. MRs that modify `.review-rules.yaml` get a visible warning

---

## Phase 5 — GitHub & GitLab Cloud Support

- **Status:** Not started
- **Completed:** —
- **Notes:** —

**Scope:** Extend provider abstraction to cover GitHub and GitLab Cloud.

**What gets built:**
- GitHub provider implementation (REST + GraphQL)
- GitLab Cloud provider implementation
- Webhook signature validation per provider type
- Provider-specific diff format normalization

**Definition of Done:**
1. Full review pipeline works end-to-end on GitHub PRs (same quality as GitLab)
2. Full review pipeline works end-to-end on GitLab Cloud MRs
3. Webhook signatures are validated; invalid payloads are rejected
4. A single deployment can have providers of all three types active simultaneously

---

## Phase 6 — Reconciliation, Feedback & Observability

- **Status:** Not started
- **Completed:** —
- **Notes:** —

**Scope:** Operational hardening and quality measurement.

**What gets built:**
- `PRReconciliation` Virtual Object — periodic sweep to catch missed webhooks
- Review feedback (map provider reactions to ratings)
- Feedback dashboard in admin console
- Structured logging with correlation IDs
- Prometheus metrics endpoints
- OpenTelemetry tracing across Go/Python
- Health check endpoints

**Definition of Done:**
1. A missed webhook is caught by reconciliation within the configured interval
2. Developer reactions on review comments are synced and visible in the dashboard
3. Metrics (review latency, token usage, success rate) are scrapable by Prometheus
4. Distributed traces span the full pipeline from webhook to posted comment
5. All services expose health check endpoints

---

## Priority Rationale

Phases 1-2 deliver the core value (automated reviews with context). Phase 3 makes it usable by non-technical admins. Phase 4 improves review quality on active MRs. Phase 5 broadens reach. Phase 6 makes it production-grade.
