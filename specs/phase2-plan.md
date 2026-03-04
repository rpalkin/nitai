# Phase 2 — Semantic Search & Context-Aware Review — Implementation Plan

## Overview

Webhook-driven automation, semantic search, and context-aware review. Adds diff-hash dedup, draft MR handling, repo cloning, indexing, and LLM tools (search + file reader).

**Prerequisite:** Phase 1 complete, `docker compose up` runs the full stack.

## Decisions

- **Webhook endpoint:** Lives in `api-server` (same binary, new route `/webhooks/<provider-id>`)
- **Repo clones:** Bare git clones on a Docker volume, managed by a new `RepoSyncer` Go handler in `go-services`
- **Indexer:** Existing Python module adapted as a Restate service handler (new entrypoint alongside CLI)
- **Search-MCP:** Runs as a persistent container with HTTP transport (SSE), called by the reviewer over the Docker network
- **File reader:** Python function inside the reviewer that shells out to `git show <sha>:<path>` on the bare clone volume
- **Debounce:** 3-minute `ctx.sleep` inside `PRReview.Run`, cancelled by webhook on new push
- **Background indexing:** `IndexMainBranch` Virtual Object with self-scheduling pattern

---

## Subphase 2.1 — Webhook Endpoint & Signature Validation ✓ Done

**Goal:** API server accepts GitLab MR webhook events with signature validation, parses the payload, and responds 200/401.

### Tasks

1. **Webhook handler route** in `api-server`
   - `POST /webhooks/:provider_id` — new HTTP handler (not ConnectRPC, plain HTTP)
   - Look up provider by ID from DB (must exist, not deleted)
   - Read raw request body for signature validation

2. **GitLab webhook signature validation**
   - GitLab sends `X-Gitlab-Token` header (shared secret, not HMAC)
   - Compare against the provider's `webhook_secret` stored in DB
   - Return 401 on mismatch

3. **Webhook secret generation**
   - On `CreateProvider`: generate a random 32-byte hex webhook secret, store in DB
   - Return the secret in the `CreateProvider` response (shown once to admin)
   - Migration `000004_webhook_secret.up.sql`: add `webhook_secret TEXT` column to `providers`

4. **Payload parsing**
   - Parse GitLab MR webhook JSON: extract `object_kind`, `object_attributes.action`, `object_attributes.iid`, `project.id`, `object_attributes.work_in_progress` / `draft`
   - Log the event, return 200 OK (no Restate dispatch yet — that's 2.2)

5. **Proto update**
   - Add `webhook_secret` to `CreateProviderResponse` (field 2), not to `Provider` message

### Implementation notes

**What changed from the plan:**

- **`webhook_secret` on proto**: Plan originally mentioned adding it to the `Provider` message. Implemented on `CreateProviderResponse` only (field 2), not on `Provider` — cleaner "show once" pattern that avoids leaking the secret via `ListProviders`.

**Architectural decisions:**

- **`WebhookStore` interface**: Handler takes a `WebhookStore` interface instead of `*pgxpool.Pool` directly. `PoolWebhookStore` is the production adapter wrapping `db.GetProvider`. Makes unit tests trivial — stub the interface, no real DB needed.

- **Nullable DB column**: `webhook_secret TEXT` (nullable) keeps backward compatibility with providers created before migration 000004. The webhook handler returns 401 if `WebhookSecret` is `nil`, gracefully handling legacy rows.

- **Atomic secret insertion**: `insertProviderTx` (the atomic provider+repos insert) was extended to include `webhook_secret` rather than a separate UPDATE — keeps provider creation fully atomic.

- **Timing-safe comparison**: Token validation uses `crypto/subtle.ConstantTimeCompare` to prevent timing oracle attacks.

- **Route on shared mux**: `/webhooks/` prefix registered on the same mux as ConnectRPC handlers (same port). The handler strips the prefix to extract `provider_id`. No separate port or listener.

### Definition of Done
- `POST /webhooks/<provider-id>` with valid `X-Gitlab-Token` returns 200
- Invalid/missing token returns 401
- Invalid provider ID returns 404
- Webhook secret is generated on provider creation and returned in the response
- Unit tests cover all cases (valid, missing token, wrong token, 404, 405, non-MR event)

### How to Test
```bash
# Create provider, note webhook_secret from response
# Send a test webhook:
curl -X POST http://localhost:8090/webhooks/<provider-id> \
  -H "X-Gitlab-Token: <secret>" \
  -H "Content-Type: application/json" \
  -d '{"object_kind":"merge_request","object_attributes":{"action":"open","iid":1},"project":{"id":123}}'
# Expect: 200 OK

# Wrong token:
curl -X POST http://localhost:8090/webhooks/<provider-id> \
  -H "X-Gitlab-Token: wrong" \
  -d '{}'
# Expect: 401
```

---

## Subphase 2.2 — Webhook → Restate Dispatch (Cancel + Submit) ✓ Done

**Goal:** Webhook handler dispatches `PRReview.Run` via Restate ingress on MR open/update events. Cancels in-flight review on new push.

### Implementation notes

**What changed from the plan:**

- **Invocation ID storage**: Cancel-on-new-push requires the Restate invocation ID, not just a service key. Added `restate_invocation_id TEXT` column to `review_runs` (migration 000005). `SendPRReview` now returns the invocation ID parsed from the `{"invocationId":"...","status":"Accepted"}` response.
- **`CreateReviewRunWithInvocation`**: Webhook creates the review run *after* dispatching (invocation ID known), unlike `TriggerReview` which creates first and updates after. Both approaches land in the same DB state.
- **`TriggerReview` updated**: Captures the invocation ID from `SendPRReview` and stores it via `UpdateReviewRunInvocationID`.
- **`RestateDispatcher` interface**: Extracted for testability — webhook handler takes an interface rather than `*restate.Client` directly. Production wires the real client; tests use `stubRestateDispatcher`.
- **`restate.New` signature change**: Now takes `(ingressURL, adminURL string)` — breaking change to constructor.
- **Cancel uses admin API**: `PATCH {adminURL}/invocations/{id}/cancel` — 404 is silently ignored (already completed).
- **Draft detection**: Draft MRs are skipped at the webhook layer (no DB record created). Full draft tracking (with DB state) is deferred to subphase 2.4.

**Architectural decisions:**

- **`WebhookStore` interface expanded**: Added `GetRepoByRemoteID`, `GetActiveInvocationID`, `CreateReviewRunWithInvocation` methods. `PoolWebhookStore` delegates to `db` package functions.
- **Best-effort cancel**: Cancel failure is logged but does not abort the dispatch. The old invocation may complete — acceptable, as the new one will supersede it.
- **No `RunID` in webhook dispatch**: The `PRReviewRequest` sent from the webhook omits `RunID` (set to empty string). The run ID is stored in DB alongside the invocation ID. The workflow receives it as empty — this may need reconciliation in a later subphase if the workflow needs the run ID.
- **`nil` dispatcher guard**: If dispatcher is nil (e.g., legacy test cases), the handler logs and returns 200 without dispatching — avoids nil panics in tests that don't set up a dispatcher.

### Tasks

1. **Event routing logic** in webhook handler
   - `object_kind == "merge_request"` only, ignore all others
   - Map `action` field: `open`, `update`, `reopen` → trigger review; others → ignore
   - Look up repo by `project.remote_id` + `provider_id` — must exist and have `review_enabled=true`

2. **Cancel + submit pattern**
   - Extend `api-server/internal/restate/client.go`:
     - `CancelPRReview(key string)` — `DELETE` or `PATCH` to Restate admin API to cancel running invocation
     - On MR event: cancel existing, then submit new via `SendPRReview`
   - Key format: `<repo_id>-<mr_number>`

3. **Draft MR detection**
   - If `object_attributes.draft == true` or `object_attributes.work_in_progress == true`: log "draft MR, skipping review", return 200 without dispatching
   - If `action == "update"` and MR transitions from draft to ready (`changes.draft.previous == true, changes.draft.current == false`): treat as trigger

### Definition of Done
- Opening a non-draft MR triggers a `PRReview.Run` invocation visible in Restate UI
- Pushing to an MR with an in-flight review cancels the old invocation and starts a new one
- Draft MRs do not trigger reviews
- Marking a draft MR as ready triggers a review
- Non-MR webhook events (push, tag, etc.) are ignored with 200

### How to Test
```bash
# Configure webhook URL in GitLab project settings pointing to http://<host>:8090/webhooks/<provider-id>
# Open a non-draft MR → check Restate UI for PRReview invocation
# Push to the MR → see old invocation cancelled, new one started
# Open a draft MR → no invocation in Restate UI
# Mark draft as ready → invocation appears
```

---

## Subphase 2.3 — Debounce & Diff-Hash Dedup ✓ Done

**Goal:** Smart debounce in PRReview.Run only delays on rapid pushes (not first trigger). Diff-hash dedup skips review if diff is unchanged.

### Tasks

1. **Debounce in `PRReview.Run`**
   - Add `ctx.Sleep(3 * time.Minute)` as the first step in the `Run` handler (before `DiffFetcher.FetchPRDetails`)
   - If the invocation is cancelled during sleep (new push arrived), Restate handles it — no cleanup needed

2. **Diff-hash computation**
   - In `DiffFetcher.FetchPRDetails`: compute SHA-256 of the unified diff string, return as `diff_hash`
   - Migration `000005_diff_hash.up.sql`: add `diff_hash TEXT` column to `review_runs`

3. **Diff-hash dedup inside `FetchPRDetails`**
   - Extend `FetchPRDetails` to also handle dedup: after computing `diff_hash`, query DB for the most recent completed review for this repo+MR
   - If `diff_hash` matches and `force == false`: set `skip=true` in the response
   - If prior review exists and not skipping: include previous review comments in the response (for context in re-review)
   - `FetchRequest` gains a `force` boolean field

4. **Wire into PRReview.Run**
   - After `FetchPRDetails`, check `skip` flag in response
   - If `skip=true`: update review_run status to `skipped`, exit
   - Store `diff_hash` on the review_run row

### Implementation notes

**What changed from the plan:**

- **Smart debounce instead of unconditional sleep**: Plan called for always sleeping 3 minutes. Implemented smart debounce using Restate Virtual Object state (`last_started_at`): only sleeps when a previous invocation was started within the last 3 minutes. First webhook trigger proceeds immediately — zero delay. On rapid second push, the first invocation is cancelled and the new one detects the recent timestamp and debounces.
- **HeadSHA used instead of SHA-256 of diff**: Plan specified SHA-256 of the unified diff string. Used `details.HeadSHA` (git commit SHA) directly as the diff hash — it's already a unique identifier and avoids fetching the full diff when skipping. The diff fetch is now skipped entirely when dedup matches.
- **Migration number 000006**: Plan mentioned 000005, but 000005 was used for `restate_invocation_id` in subphase 2.2. Used 000006.
- **`GetMRDetails` called before `GetMRDiff`**: To enable early-exit on dedup match without fetching the diff. The diff fetch only happens when the review will proceed.

**Architectural decisions:**

- **`skipped` added to `review_status` enum**: `ALTER TYPE review_status ADD VALUE IF NOT EXISTS 'skipped'` in migration 000006. Down migration only drops the column (enum values can't be removed in PostgreSQL).
- **`Force` field on both `PRReviewRequest` and `FetchRequest`**: `api-server`'s `TriggerReview` passes `Force: true`; webhook dispatch leaves it at the zero value (`false`). The field propagates from `PRReview.RunRequest` → `DiffFetcher.FetchRequest`.
- **Diff hash stored before marking running**: `UpdateReviewRunDiffHash` is called after confirming the review will proceed (not skipped) but before marking status=running. This ensures the hash is persisted even if subsequent steps fail.

### Definition of Done
- Two rapid pushes (< 3 min apart) result in only one review
- Reopening an MR with no code changes → review is skipped (status=skipped in DB)
- API-triggered reviews are forced by default (always run, never skip on diff-hash match)
- `review_runs` table has `diff_hash` populated

### How to Test
```bash
# Push twice within 1 minute → only one review completes (check Restate UI)
# Webhook-triggered review on unchanged MR → status=skipped
# API-triggered review (TriggerReview) → always completes (force=true by default)
```

---

## Subphase 2.4 — Draft MR Tracking ✓ Done

**Goal:** Draft MRs are tracked in the database but not reviewed. Marking ready triggers review.

### Tasks

1. **Migration `000006_draft_status.up.sql`**
   - Add `draft` value to `review_runs.status` enum (or add a `is_draft BOOLEAN` column on a new `pr_tracking` table)
   - Simpler approach: add `draft` to existing status enum on `review_runs`

2. **Webhook handler: draft tracking**
   - On draft MR open/update: upsert a `review_runs` row with `status=draft` (no Restate dispatch)
   - On "marked ready" event: update status from `draft` to `pending`, dispatch `PRReview.Run`

3. **PRReview.Run: draft guard**
   - At the start of `Run` (after debounce, before fetch): check if MR is currently a draft via provider API
   - If draft: update status to `draft`, exit early
   - This handles the race where MR was marked as draft between webhook receipt and review execution

### Definition of Done
- Opening a draft MR creates a review_run with `status=draft` in DB
- No Restate invocation is created for draft MRs
- Marking a draft MR as ready transitions status to `pending` and triggers review
- If MR becomes draft during debounce window, review exits early

### How to Test
```bash
# Open a draft MR → check DB: review_runs.status = 'draft'
# No invocation in Restate UI
# Mark as ready → review_runs.status changes, review executes
# Verify comment appears on the MR
```

### Implementation notes

**What changed from the plan:**

- **Migration number 000007** (not 000006 as mentioned in the plan — 000006 was used for `diff_hash` in 2.3). Migration adds `draft` to `review_status` enum using `ALTER TYPE ... ADD VALUE IF NOT EXISTS`.
- **Repo lookup moved before draft check**: Handler reorder was needed — `GetRepoByRemoteID` now runs before the draft check so that `repoID` is available for both `CreateDraftReviewRun` and `TransitionDraftToReview`.
- **Draft guard uses `DiffFetcher` response**: The `Draft` field is returned in `FetchResponse` (populated from `details.Draft`), so `PRReview.Run` doesn't need a separate provider API call to check draft status.

**Architectural decisions:**

- **`CreateDraftReviewRun` and `TransitionDraftToReview` in `WebhookStore` interface**: Two new methods added to `WebhookStore` (and `PoolWebhookStore`). Both delegate to new `db` package functions.
- **`TransitionDraftToReview` is idempotent**: Updates at most one row (the most recent `draft` row for the repo+MR). No-op if no draft row exists — safe to call even if the draft wasn't tracked.
- **`Draft` field added to `MRDetails` and `FetchResponse`**: GitLab API returns `draft: true/false` on the MR object; mapped through `gitlabMR.Draft` → `MRDetails.Draft` → `FetchResponse.Draft`.
- **Draft guard returns `runID` (not empty string)**: `PRReview.Run` returns `runID, nil` on draft guard to preserve the run ID for any callers.

---

## Subphase 2.5 — Repo Syncer Service ✓ Done

**Goal:** Go Restate handler that maintains bare git clones on a shared Docker volume.

### Tasks

1. **`RepoSyncer` Restate service**
   - `go-services/internal/reposyncer/service.go`
   - Handler: `SyncRepo(repo_id) -> SyncResult`
   - Reads repo info from DB (provider credentials, clone URL)
   - Clone URL: construct from provider `base_url` + repo `full_name` (e.g., `https://gitlab.example.com/group/project.git`)
   - Auth: embed token in clone URL (`https://oauth2:<token>@gitlab.example.com/...`) for HTTPS clone

2. **Bare clone management**
   - Storage path: `/data/repos/<repo_id>/` on a Docker volume — one bare clone per repo
   - A bare clone contains all refs for all branches, so concurrent reviews targeting different branches share the same clone safely (`git show <sha>:<path>` on a bare repo is read-only and branch-independent)
   - If directory doesn't exist: `git clone --bare <url> <path>`
   - If exists: `git fetch origin` to update all refs (fetches all branches in one call)
   - Disable auto-GC: set `gc.auto=0` on clone (per `later.md` concern)
   - `SyncRepo` input includes `target_branch`; return `head_sha` of that branch (`git rev-parse origin/<target_branch>`)

3. **Docker volume**
   - Add `repos` volume to `docker-compose.yml`
   - Mount at `/data/repos` in `worker` and `reviewer` containers

4. **Register `RepoSyncer` with Restate** in `cmd/worker/main.go`

### Definition of Done
- `RepoSyncer.SyncRepo` clones a repo on first call, fetches on subsequent calls
- Clone is bare, stored at `/data/repos/<repo_id>/`
- `git show <sha>:<path>` works against the bare clone
- Volume persists across container restarts

### How to Test
```bash
# Invoke SyncRepo via Restate admin API or add a test call in PRReview.Run
# Check: /data/repos/<repo_id>/ exists with .git bare structure
# Run again: git fetch (no re-clone), fast completion
# Verify: git --git-dir=/data/repos/<repo_id> show HEAD:README.md returns content
```

### Implementation notes

**What changed from the plan:**

- **go-git instead of shell-out**: The plan mentioned `git clone --bare` and `git fetch origin` via CLI. Used `github.com/go-git/go-git/v5` (pure Go) instead — no shell-out needed, no `gc.auto=0` concern (go-git doesn't run GC automatically).
- **`refs/heads/<branch>` not `refs/remotes/origin/<branch>`**: Bare clones via go-git store remote branches at `refs/heads/*` (mirroring the remote directly), not at `refs/remotes/origin/*`. The plan specified `refs/remotes/origin/<target_branch>` for revision resolution; implementation uses `refs/heads/<target_branch>`.
- **Explicit RefSpecs on fetch**: go-git's bare clone configures the `origin` remote with a non-bare default refspec (`+refs/heads/*:refs/remotes/origin/*`). Added explicit `RefSpecs: []config.RefSpec{"+refs/heads/*:refs/heads/*"}` to `FetchContext` to ensure consistency between initial clone and subsequent fetches.
- **`syncBareRepo` helper extracted**: Git sync logic extracted into a testable `syncBareRepo(ctx, repoPath, cloneURL, token)` helper separate from DB/crypto concerns.
- **Auth not embedded in URL**: Plan mentioned embedding token in clone URL. Used `go-git`'s `http.BasicAuth{Username: "oauth2", Password: token}` passed via `CloneOptions.Auth` / `FetchOptions.Auth` — cleaner separation.

**Architectural decisions:**

- **`buildCloneURL` helper**: URL construction extracted to a pure function (`net/url.Parse` + `path.Join`), fully unit-tested with table-driven tests.
- **`RepoSyncer` registered as regular Service** (not Virtual Object): stateless, no per-repo state needed in Restate. Concurrency on the same repo_id is safe — go-git clone is atomic (writes to temp dir + rename) and fetch is read-safe.
- **Remote URL update on fetch**: If `origin` URL differs from the computed clone URL (e.g., provider base URL migrated), it's updated in the repo config before fetching.

---

## Subphase 2.6 — Indexer as Restate Service 🧪 In test

**Goal:** Adapt the existing indexer module as a Restate Python service handler.

### Tasks

1. **Restate handler wrapper**
   - `indexer/indexer/service.py` — new file
   - Restate service: `Indexer` with handler `IndexRepo(IndexRequest) -> IndexResult`
   - `IndexRequest`: repo_id, repo_path (on volume), branch, collection_name
   - Wraps existing `main.py` indexing logic (tree-sitter chunking, embedding, Qdrant upsert)

2. **Migration `000007_branch_index.up.sql`**
   - `branch_indexes` table: id, repo_id, branch, last_indexed_commit, collection_name, updated_at
   - Unique constraint on (repo_id, branch)

3. **Incremental indexing**
   - Before indexing: check `branch_indexes` for `last_indexed_commit`
   - If commit exists: `git diff --name-only <last_commit>..<current_commit>` to find changed files
   - Only re-index changed files (delete old chunks, insert new)
   - Update `branch_indexes.last_indexed_commit` after success

4. **Collection naming**
   - Collection name: `<repo_id>-<branch>` (sanitized)
   - Use existing `sanitize_collection_name` from indexer module

5. **Adapt indexer to work with bare clones**
   - The existing indexer walks a working tree (`os.walk`). Bare clones have no working tree.
   - Adapt the indexer to enumerate files via `git ls-tree -r --name-only <sha>` and read content via `git --git-dir=<repo_path> show <sha>:<file_path>` instead of filesystem reads
   - This is the same pattern as the file reader tool — all file access goes through `git show` on the bare clone

6. **Docker Compose updates**
   - Add `indexer` as a Restate service (new container, always running, not profile-gated)
   - Mount `repos` volume at `/data/repos` (bare clones, accessed via `git show`)
   - Register indexer deployment with Restate in `restate-register`

7. **Qdrant added to default Compose profile** (currently exists but may be profile-gated)

### Definition of Done
- `Indexer.IndexRepo` indexes a branch from a bare clone into Qdrant
- Second call with same commit is a no-op (incremental: no work if unchanged)
- Second call with new commit only re-indexes changed files
- `branch_indexes` table tracks last indexed commit
- Qdrant collection created with correct name

### How to Test
```bash
# After SyncRepo, invoke Indexer.IndexRepo
# Check Qdrant: collection exists with vectors
curl http://localhost:6333/collections/<collection-name>
# Re-invoke: check logs show "no changes" or "incremental: N files"
# Search via search-mcp client:
python search-mcp/client.py search <collection> "function name" --top-k 3
```

### Implementation notes

**What changed from the plan:**

- **CLI removed:** The plan mentioned keeping the CLI for backwards compat. Removed `main.py` and top-level `splitter.py` entirely — all indexing goes through the Restate service now.
- **`click` and `python-dotenv` removed:** No longer needed (CLI gone). Package deps trimmed to only what the service requires.
- **Migration number 000008** (not 000007 as in the plan body — 000007 was used for `draft_status` in 2.4).
- **Python 3.12 in Dockerfile:** Upgraded from 3.11-slim to 3.12-slim (matching reviewer).

**Architectural decisions:**

- **Stateless indexer:** Go caller owns `branch_indexes` table access. Indexer receives `last_indexed_commit` in `IndexRequest` and returns `IndexResult`. No DB connection in the Python service.
- **Skip-if-unchanged in `index_repo()`:** Both Go caller (skips Restate call entirely) and `index_repo()` (returns no-op early) check for `last_indexed_commit == head_sha` — defense in depth.
- **`git --git-dir` flag throughout:** All git commands in `git.py` use `--git-dir=<repo_path>` for bare clone access at `/data/repos/<repo_id>/`.

---

## Subphase 2.7 — File Reader Tool for Reviewer

**Goal:** Reviewer agent can read files at a pinned SHA from the bare clone during review.

### Tasks

1. **File reader function**
   - `reviewer/reviewer/tools.py` — new file
   - `read_file(repo_path: str, sha: str, file_path: str) -> str`
   - Executes `git --git-dir=<repo_path> show <sha>:<file_path>`
   - Returns file content as string, or error message if file doesn't exist
   - Sanitize inputs: validate sha is hex, file_path has no `..` traversal

2. **Register as Pydantic AI tool**
   - In `agent.py`: add `read_file` as a tool on the agent
   - Tool description: "Read a file from the repository at the review target branch HEAD"
   - Parameters: `file_path` (the path in the repo)
   - `sha` and `repo_path` injected from review context (not exposed to LLM)

3. **Pass clone path + SHA to reviewer**
   - Extend `ReviewRequest` model: add `repo_path` (str, optional), `target_branch_sha` (str, optional)
   - `PRReview.Run` passes these after `SyncRepo` completes

4. **Update system prompt**
   - Tell the reviewer it can read files outside the diff using the `read_file` tool
   - Guide: use it when a diff references functions/types defined elsewhere

### Definition of Done
- During review, the LLM can call `read_file` and receive file content
- File reads are pinned to the target branch HEAD SHA (no race with concurrent fetches)
- Invalid paths return a clean error message (not a stack trace)
- `..` traversal in file paths is rejected

### How to Test
```bash
# Trigger a review on an MR that calls a function defined in another file
# Check review output: reviewer should reference code from outside the diff
# Check logs: read_file tool calls visible
# Test invalid path: ensure no error propagation to MR comment
```

---

## Subphase 2.8 — Search Tool Integration for Reviewer

**Goal:** Reviewer agent can semantically search the codebase via search-MCP during review.

### Tasks

1. **Search-MCP as a persistent HTTP container**
   - Run `search-mcp` as a separate always-on Docker container (already exists in Compose, promote from profile to default)
   - Switch transport from MCP stdio to HTTP (SSE or streamable-http) — FastMCP supports this with `mcp.run(transport="sse")`
   - Expose on an internal Docker network port (e.g., `:8081`)
   - `reviewer/reviewer/tools.py` — add `SearchMCPClient` class that connects to the search-mcp container via HTTP
   - Methods: `search(collection: str, query: str, top_k: int) -> list[SearchResult]`
   - No subprocess lifecycle management needed — the container is always running

2. **Register as Pydantic AI tool**
   - `search_codebase(query: str) -> list[SearchResult]`
   - Collection name injected from context (derived from repo_id + target branch)
   - `top_k` defaults to 5

3. **Pass collection info to reviewer**
   - Extend `ReviewRequest`: add `search_collection` (str, optional)
   - `PRReview.Run`: after `IndexRepo`, pass the collection name

4. **Update system prompt**
   - Tell the reviewer it can search the codebase for related code
   - Guide: use search when the diff modifies or calls code whose full context isn't in the diff

5. **Docker networking**
   - Ensure the reviewer container can reach the search-mcp container (Docker network)
   - Ensure the search-mcp container can reach Qdrant (already on the same network)

### Definition of Done
- During review, the LLM can call `search_codebase` and receive semantically relevant code snippets
- Search results include file path, content snippet, and relevance score
- Search-MCP container is healthy and reachable from the reviewer container
- Reviews that use search produce comments referencing code outside the diff

### How to Test
```bash
# Index a repo, then trigger a review on an MR that touches a function used elsewhere
# Check review output: should mention related code found via search
# Check logs: search tool calls with queries and results
# Verify search-mcp container stays healthy across multiple reviews
```

---

## Subphase 2.9 — Wire Full Pipeline (PRReview.Run v2)

**Goal:** `PRReview.Run` orchestrates the complete Phase 2 pipeline: debounce → fetch → dedup → sync → index → review (with tools) → post.

### Tasks

1. **Update `PRReview.Run` handler** to call new services in order:
   1. `ctx.Sleep(3m)` — debounce
   2. `DiffFetcher.FetchPRDetails` — diff + metadata + diff_hash + dedup check (skip if unchanged)
   3. Draft check via provider API (if still draft, exit)
   4. `RepoSyncer.SyncRepo` — clone/fetch repo
   5. `Indexer.IndexRepo` — index target branch
   6. `Reviewer.RunReview` — with search_collection, repo_path, target_branch_sha
   7. `PostReview.Post` — post comments

2. **Pass new fields through the pipeline**
   - After `SyncRepo`: capture `head_sha`, `repo_path`
   - After `IndexRepo`: capture `collection_name`
   - Build enriched `ReviewRequest` with all fields

3. **Error handling**
   - If `SyncRepo` fails: mark review as failed, don't proceed
   - If `IndexRepo` fails: proceed with review but without search (log warning, set `search_collection=""`)
   - Review should still work without search (graceful degradation)

4. **Update Restate registration** — ensure all new services (RepoSyncer, Indexer) are registered

### Definition of Done
- End-to-end: webhook → debounce → fetch → dedup → sync → index → review with tools → post comments
- Rapid pushes result in one review (debounce works)
- Unchanged diff is skipped (dedup works)
- Review comments show evidence of codebase context (search/file reads used)
- If indexing fails, review still completes (without search)

### How to Test
```bash
# Full end-to-end:
# 1. Create provider with webhook secret
# 2. Configure webhook in GitLab
# 3. Enable review for a repo
# 4. Open an MR
# 5. Wait ~3 minutes (debounce)
# 6. Check: review comments appear on MR
# 7. Push again quickly twice → only one new review
# 8. Verify in Restate UI: pipeline steps visible in invocation timeline
```

---

## Subphase 2.10 — Background Indexing (IndexMainBranch)

**Goal:** Primary branch is indexed automatically on push and on a periodic schedule, keeping the vector index warm.

### Tasks

1. **`IndexMainBranch` Virtual Object**
   - `go-services/internal/indexmainbranch/service.go`
   - Virtual Object keyed by `<repo_id>`, exclusive `Run` handler
   - Steps: `SyncRepo` → `IndexRepo` (primary branch) → schedule next run (`ctx.Send` with 6h delay)

2. **Initial trigger on repo enable**
   - When `EnableReview` is called: submit initial `IndexMainBranch.Run` invocation
   - This kicks off the self-scheduling loop (runs immediately, then every 6h)

3. **On-demand indexing in PRReview.Run**
   - The `IndexRepo` call in the PR review pipeline (subphase 2.9) handles on-demand indexing when the scheduled index is stale
   - No webhook trigger for push events — the 6h schedule + on-demand before review is sufficient

4. **Register with Restate** in `cmd/worker/main.go`

### Definition of Done
- Enabling review for a repo triggers initial indexing of the primary branch
- After indexing, a 6-hour delayed self-invocation is scheduled
- Restate UI shows the scheduled delayed invocation
- PRReview pipeline's IndexRepo step is fast (no-op) when the primary branch was recently indexed
- If index is stale at review time, PRReview's IndexRepo call updates it on-demand

### How to Test
```bash
# Enable review for a repo → check Qdrant: collection created
# Check Restate UI: IndexMainBranch invocation with 6h delayed follow-up
# Trigger a review: IndexRepo step should complete quickly (already indexed)
# Wait for 6h schedule (or simulate): re-indexing runs automatically
```

---

## E2E Test Framework (Infrastructure) ✓ Done

A standalone Go e2e test module was created at `e2e/` as Phase 2 infrastructure. It provides:

- Mock GitLab API server (`mock_gitlab.go`) — configurable per-MR responses, thread-safe request recording
- Mock LLM server (`mock_llm.go`) — OpenAI-compatible, returns tool-calling format responses
- Full-stack test harness via testcontainers-go compose (`docker-compose.e2e.yml` overlay)
- ConnectRPC test clients and polling helpers (`helpers.go`)
- `TestFullPipelineViaTriggerReview` as the first test case

Run with `make e2e` (requires Docker). 28 planned test cases in `specs/e2e-cases.md`. See `e2e/CLAUDE.md` for details.

---

## Subphase 2.11 — E2E Tests & Smoke Tests Update

**Goal:** Update test scripts to cover Phase 2 functionality.

### Tasks

1. **Update `tests/smoke.sh`**
   - Verify new services registered: `RepoSyncer`, `Indexer`, `IndexMainBranch`
   - Verify Qdrant is reachable
   - Verify repos volume is mounted

2. **Update `tests/e2e.sh`**
   - Test webhook delivery: create provider, configure webhook, send synthetic MR event
   - Test debounce: send two events rapidly, verify single review
   - Test diff-hash dedup: trigger same MR twice, verify skip
   - Test draft handling: send draft MR event, verify no review; send ready event, verify review
   - Test search integration: verify review comments reference code outside diff

3. **New `tests/webhook_test.sh`** (optional, can be folded into e2e)
   - Focused webhook validation tests (signature, payload parsing, routing)

### Definition of Done
- `make smoke` passes with all Phase 2 services registered
- `make e2e` covers webhook-triggered reviews end-to-end
- Tests verify debounce, dedup, draft handling, and search integration
- All tests pass in CI (or documented as requiring GitLab env vars)

### How to Test
```bash
make smoke   # All services registered, Qdrant reachable
make e2e     # Full Phase 2 pipeline tested
```

---

## Phase 2 Definition of Done (all subphases complete)

1. Configuring the webhook URL in GitLab and opening/updating an MR triggers the review pipeline automatically
2. Rapid sequential pushes to an MR result in exactly one review (debounce + cancel-on-new-push)
3. Webhook signatures are validated; invalid payloads are rejected
4. Reopening an MR with no code changes skips the review (diff-hash match)
5. Draft MRs are tracked but not reviewed; marking ready triggers review
6. Enabling review for a repo triggers initial indexing of the primary branch
7. The reviewer can search the codebase and read files outside the diff during review
8. Review quality demonstrably improves on PRs that depend on code not in the diff
9. Re-indexing is incremental (only changed files)
10. Background re-indexing runs on a 6-hour schedule and on-demand before MR review

## Dependencies & Key Libraries

| Component | New Dependencies |
|---|---|
| api-server | webhook handler (plain net/http), crypto/rand (secret generation) |
| go-services | os/exec (git commands), RepoSyncer service |
| indexer | restate-sdk (Python), git CLI |
| reviewer | search-mcp (subprocess), git CLI |
| infra | Qdrant (promoted to default profile), repos Docker volume |
