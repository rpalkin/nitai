# Review Pipeline

The PR review pipeline orchestrates the complete end-to-end review workflow, from webhook trigger to comment posting.

## Architecture

### PRReview Virtual Object

**Virtual Object:** `PRReview`
**Key:** `<repo_id>-<pr_number>`
**Handler:** `Run` (exclusive — one execution per key at a time)

When a new push arrives for a PR that is already being reviewed, the webhook handler cancels the running invocation and submits a new one. This ensures only the latest push is reviewed.

```
Webhook received (API Server)
   |
   +~- Cancel running invocation (if any):
   |    PATCH /invocations/PRReview/<key>/Run/cancel
   |
   v
Submit new invocation:
  POST /PRReview/<key>/Run
   |
   v
+---------------------------------------------+
| Debounce (3 minutes)                         |
|  - ctx.sleep(3m) before proceeding           |
|  - if a new push arrives during the wait,    |
|    the webhook handler cancels this          |
|    invocation and starts a fresh one          |
|  - rapid pushes only pay the cost of one     |
|    review after the author stops pushing     |
+----------------------+----------------------+
                        |
                        v
+---------------------------------------------+
| Call: DiffFetcher.FetchPRDetails             |
|  - call provider API for diff, metadata     |
|  - compute diff_hash                        |
|  - count changed lines                      |
|  - if changed lines > 5,000 ~> set           |
|    diff_too_large flag                      |
+----------------------+----------------------+
                        |
                        v
                +--------------+
                |diff_too_large?|
                +------~-------+
                  yes |   no
           +-----------+-----------+
           v                       v
+------------------+  +---------------------------------------------+
| Call:            |  | Call: DiffFetcher.CheckPreviousReview        |
| PostReview.Post: |  |  - query DB for prior review of same PR     |
| "PR too large    |  |  - if diff_hash unchanged and not force     |
|  for automated   |  |    ~> skip, exit                             |
|  review"         |  |  - if prior review exists ~> load previous   |
| ~> exit          |  |    comments from DB (don't repeat)          |
+------------------+  +----------------------+----------------------+
                                              |
                                              v
+---------------------------------------------+
                       | Call: RepoSyncer.SyncRepo                   |
                       |  - clone or fetch bare git repo             |
                       |  - returns repo_path and head_sha           |
                       +----------------------+----------------------+
                                               |
                                               v
                       +---------------------------------------------+
                       | Local merge of source into target            |
                       |  - compute local merge commit via           |
                       |    git merge-tree --write-tree              |
                       |  - if conflicts ~> status=conflicts, exit   |
                       |  - merge_sha used for file reader and       |
                       |    search indexing                           |
                       +----------------------+----------------------+
                                               |
                                               v
                       +---------------------------------------------+
                       | Read: .review-rules.yaml from bare clone     |
                       |  - read file at merge result commit SHA     |
                       |  - parse ignore globs + custom instructions |
                       |  - if file not found ~> silent no-op       |
                       |  - if modified in PR ~> set rules_modified  |
                       +----------------------+----------------------+
                                              |
                                              v
                      +---------------------------------------------+
                      | Resolve: org-level instructions from DB     |
                      |  - query review_instructions for org         |
                      |  - filter by repo_filter + file_pattern     |
                      |  - non-fatal: on error, proceed with        |
                      |    YAML instructions only                   |
                      +----------------------+----------------------+
                                              |
                                              v
                      +---------------------------------------------+
                      | Filter diff by ignore globs                 |
                      |  - remove files matching ignore patterns    |
                      |  - if no reviewable files remain ~> skip     |
                      +----------------------+----------------------+
                                              |
                                              v
                      +---------------------------------------------+
                      | Diff too large after filtering?              |
                      |  - if filtered changed lines > 5000         |
                      |    ~> post "too large" comment and exit      |
                      +----------------------+----------------------+
                                              |
                                              v
                      +---------------------------------------------+
                      | Call: Indexer.IndexRepo                      |
                      |  - check BranchIndex for target branch       |
                      |  - if unchanged ~> skip indexing             |
                      |  - if target ≠ primary branch and no        |
                      |    collection exists ~> clone primary branch |
                      |    collection, reindex only differing files |
                      |  - otherwise full incremental index         |
                      |  - upsert BranchIndex record                |
                      +----------------------+----------------------+
                                              |
                                              v
                      +---------------------------------------------+
                      | Call: Reviewer.RunReview                     |
                      |  - invoke Pydantic AI agent with:           |
                      |    . filtered PR diff                       |
                      |    . previous comments (if any)             |
                      |    . custom instructions:                   |
                      |      - org-level (from DB, filtered by     |
                      |        repo + file pattern)                 |
                      |      - repo-level (from .review-rules.yaml)|
                      |    . MCP tools: search + file reader        |
                      |  - returns structured review output         |
                      +----------------------+----------------------+
                                              |
                                              v
                      +---------------------------------------------+
                      | Call: PostReview.Post                        |
                      |  - if dry_run ~> store results, skip posting |
                      |  - if rules_modified ~> prepend warning to  |
                      |    summary: "This PR modifies               |
                      |    .review-rules.yaml..."                   |
                      |  - post summary comment (first review only; |
                      |    on re-reviews the original stays as-is)  |
                      |  - post new inline comments via provider API |
                      |    (idempotent: each comment is stored in DB |
                      |    before posting; on retry, comments with   |
                      |    a provider_comment_id are skipped)        |
                      |  - update existing comments when the LLM    |
                      |    output includes an update action with a  |
                      |    previous comment ID                      |
                      |  - previous comments not mentioned by the   |
                      |    LLM in the new review are left as-is     |
                      |    (assumed still relevant or already        |
                      |    resolved by the author)                  |
                      |  - store review + comments (with body) in DB|
                      |  - log activity                             |
                      +---------------------------------------------+
```

### Activity Logging

User-initiated actions are logged to `activity_logs` via the API server:

| Event | Handler | Details |
|---|---|---|
| `provider.created` | `ProviderHandler.CreateProvider` | `{provider_id, provider_name, provider_type}` |
| `provider.deleted` | `ProviderHandler.DeleteProvider` | `{provider_id, provider_name}` |
| `repo.review_enabled` | `RepoHandler.EnableReview` | `{repo_name, repo_full_path}` |
| `repo.review_disabled` | `RepoHandler.DisableReview` | `{repo_name, repo_full_path}` |
| `review.triggered` | `ReviewHandler.TriggerReview` | `{review_run_id, mr_number}` |

Pipeline completion events (`review.completed`, `review.failed`) happen in go-services which lacks org context. These are tracked via `review_runs.status` and queryable via `GetReviewRun`.

Webhook-triggered reviews don't log `review.triggered` because webhooks are unauthenticated (no `actor_id`).

### DiffFetcher

**Tech:** Go handler in `DiffFetcher` service
**Purpose:** Fetches PR diff and metadata (title, description, changed files, commits) from provider APIs
**Diff size limit:** If the diff exceeds **5,000 changed lines**, the handler returns a `diff_too_large` flag. The workflow skips the review and posts a single comment explaining the PR is too large for automated review.
**Providers:** GitHub REST/GraphQL, GitLab REST — abstracted behind a common interface

### Reviewer

**Tech:** Python Restate service, Pydantic AI
**Purpose:** Runs LLM-based review of a PR
**LLM:** OpenRouter (not user-configurable)
**Inputs:** PR diff (filtered by ignore rules), previous review comments, custom instructions (from DB + repo-level `.review-rules.yaml`), repo metadata
**Tools available to LLM:**
  - Search-MCP — semantic search across the indexed codebase
  - File reader — reads files at a pinned commit SHA via `git show <sha>:<path>` on the bare clone. The SHA is the locally-computed **merge result commit** (merging source branch into target), not just target HEAD. This ensures the reader sees exactly what the repo will look like after the PR is merged, including any changes from the target branch that happened after the PR was opened.
**Output:** structured review (summary + list of inline comments with file, line, message, and optional update action referencing a previous comment ID)
**Dry-run mode:** When triggered via the Admin Console, the reviewer returns its output without posting comments. This allows admins to preview and tune review quality before enabling live reviews.
**OpenRouter resilience:** Retries use exponential backoff with jitter, respecting OpenRouter rate-limit headers (`Retry-After`, `X-RateLimit-Reset`). Restate's built-in retry policy handles transient failures. If the model is unavailable after retries, the invocation fails and can be inspected/restarted via the Restate UI.

## Custom Instructions

Instructions come from **two sources**, merged at review time:

### Organization Instructions (Admin Console)
- Organization admins write free-text review instructions via the Admin Console
- Each instruction has optional filter rules:
  - Repository filter (`repo_filter UUID[]` — specific repo IDs, or empty for all)
  - File pattern filter (`file_pattern_filter TEXT[]` — globs, e.g., `*.go`, `src/*.ts`; uses Go `filepath.Match`, no `**` support)
  - `enabled` flag — disabled instructions are excluded from resolution
- At review time, `ResolveInstructions` RPC returns applicable instructions based on the PR's repo and changed files (AND logic: both repo and file filters must match if set; empty filter = match all)
- Language filtering is handled on the frontend by mapping languages to file pattern globs

### Pipeline Integration
- The `PRReview.Run` handler resolves org-level instructions via a direct DB query (`ResolveInstructionsForRepo` in `go-services/internal/db/queries.go`)
- Resolution uses the **filtered** changed files list (after `.review-rules.yaml` ignore globs are applied) to match against `file_pattern_filter`
- Org-level instructions are fetched after `SyncRepo` completes (Step 7b in the pipeline) and resolved after diff filtering (Step 11)
- The merged instructions list (org instructions first, then repo-level instructions) is passed to the Reviewer service as `custom_instructions`

### Repository-Level Rules (`.review-rules.yaml`)
- Developers can commit a `.review-rules.yaml` file to the repository root
- This file is read from the **bare git clone** at the PR head commit SHA during the review pipeline, after RepoSyncer.SyncRepo completes. Reading directly from the local clone avoids an extra provider API call.
- If the PR modifies `.review-rules.yaml`, the `rules_modified` flag is set, which adds a warning to the posted summary.
- Format:

```yaml
# .review-rules.yaml

# Additional review instructions injected into the LLM prompt
instructions:
  - "Always check for proper error handling in Go code"
  - "Ensure all public API endpoints have authentication middleware"
  - "Flag any direct SQL queries — prefer using the query builder"

# Files matching these globs are excluded from review entirely.
# The reviewer will not see or comment on these files.
ignore:
  - "vendor/**"
  - "**/*.generated.go"
  - "**/*.pb.go"
  - "**/node_modules/**"
  - "**/__snapshots__/**"
  - "*.min.js"
  - "*.min.css"
```

- **Ignore rules** filter the diff before it reaches the reviewer — ignored files are stripped from the diff entirely, not just suppressed in output
- **Instructions** from the YAML are appended to the organization-level instructions in the system prompt
- If the file does not exist, no repo-level rules are applied (silent no-op)

## Review Feedback

Developers can provide feedback on individual review comments to measure and improve review quality over time.

- **Feedback mechanism:** Developers react to review comments on the provider (e.g., GitHub reactions). A periodic sync fetches reactions and maps them to `ReviewFeedback` records in the DB.
- **Dashboard:** The Admin Console shows aggregate feedback metrics — acceptance rate per repo, per instruction set, and over time.
- **Use cases:**
  - Identify low-quality instruction sets that produce unhelpful comments
  - Track review quality trends as instructions are tuned
  - Provide data for future fine-tuning or prompt optimization

## Key Decisions

- **Virtual Object keyed by `<repo_id>-<pr_number>`** for exclusive execution — prevents concurrent reviews of the same PR
- **3-minute debounce via `ctx.sleep`** (configurable via `DEBOUNCE_TIMEOUT`) — collapses rapid pushes into a single review
- **Diff-hash dedup** — skip if unchanged unless force flag is set
- **5,000 line diff size limit** — prevents overwhelming LLM context
- **Summary posted only on first review** — on re-reviews, the original summary stays as-is
- **Idempotent comment posting via `provider_comment_id`** — comments stored in DB before posting; on retry, comments with a provider_comment_id are skipped
- **Local merge before review** — computes a local merge commit using `git merge-tree --write-tree`, ensuring file reader and search see the exact post-merge state. Merge conflicts result in `status=conflicts` and skip the review.
- **File reader pins to merge result commit** — ensures file content shows the post-merge state, combining PR changes with any target branch updates