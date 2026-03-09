# e2e — End-to-End Test Suite

Standalone Go module (`ai-reviewer/e2e`) containing full-stack tests for the ai-reviewer system.

## Build tag

All test files use `//go:build e2e`. Tests are excluded from normal `go test ./...` runs.

## Running

```bash
# From repo root (requires Docker):
# IMPORTANT: Always run docker compose down first to ensure clean state:
docker compose -p e2e down -v
make e2e

# Equivalent direct command:
cd e2e && GOWORK=off go test -v -tags e2e -count=1 -timeout 300s ./...

# Live mode (real GitLab, real LLM):
E2E_LIVE=1 GITLAB_URL=... GITLAB_TOKEN=... make e2e

# Run specific test(s) by name (accepts Go regex pattern, e.g., TEST_INCLUDE="TestCancel|TestDebounce"):
TEST_INCLUDE=TestCancelOnNewPush make e2e

# Run multiple tests matching a pattern:
TEST_INCLUDE="TestFullPipeline|TestSemantic" make e2e

# If tests fail, the Docker stack is NOT torn down (for debugging):
# Check logs: docker compose -p e2e logs -f
# Clean up manually when done: docker compose -p e2e down -v

# To force cleanup even on failure, run manually:
# cd e2e && E2E_KEEP_STACK=0 GOWORK=off go test -v -tags e2e ./...

# Rebuild Docker images after code changes (MUST use -p e2e to match testcontainers project name):
docker compose -p e2e build api-server worker
```

**Prerequisites:** Docker must be running. `gen/go/` must be generated (`make proto` from repo root).

## Module setup

- Standalone module: NOT part of `go.work`. Always run with `GOWORK=off`.
- Uses `replace ai-reviewer/gen => ../gen/go` in `go.mod`.
- Dependencies: `testcontainers-go` (compose), `connectrpc/connect-go`, `pgx/v5` (direct DB assertions).
- `gen/go/go.mod` is committed (not gitignored) so `go get` works without running `make proto`. The generated `.go` files are still gitignored — run `make proto` before building.

## Mock mode vs live mode

| | Mock mode (default) | Live mode (`E2E_LIVE=1`) |
|---|---|---|
| GitLab | `mock_gitlab.go` — httptest server | Real GitLab instance |
| LLM | `mock_llm.go` — returns tool-call responses | Real OpenRouter / LLM |
| Docker stack | Started via testcontainers-go compose | Must be running externally |

## Key files

| File | Purpose |
|---|---|
| `e2e_test.go` | `TestMain` + test cases (build tag: `e2e`) |
| `mock_gitlab.go` | httptest-based mock GitLab API — configurable per-MR responses, thread-safe request recording |
| `mock_llm.go` | Mock OpenAI-compatible LLM server — returns tool-calling format responses |
| `helpers.go` | `TestClients` (ConnectRPC), `PollReviewRun`, `SetupProviderAndRepo`, `StartStack`/`StopStack`, `QueryReviewRuns`/`WaitForReviewRun` (direct DB) |
| `docker-compose.e2e.yml` | Overlay: sets `OPENROUTER_BASE_URL` + `extra_hosts` to reach mock servers from containers |

## Adding test cases

See `specs/e2e-cases.md` for the full list of test cases (29 implemented, 5 skipped).

Each test case should:
1. Configure the mock servers for the scenario (e.g., specific MR diff, draft status)
2. Trigger the action (webhook event or API call via `TestClients`)
3. Poll for completion with `PollReviewRun` (TriggerReview path) or `WaitForReviewRun` (webhook path)
4. Assert on review run status, posted comments, or Restate invocation state

### Polling helpers

| Helper | Use when |
|---|---|
| `PollReviewRun(t, client, runID, wantStatus, timeout, interval)` | You have a runID from `TriggerReview` |
| `WaitForReviewRun(t, repoID, mrNumber, wantStatus, timeout)` | You triggered via webhook (no runID returned) or need `draft`/`skipped` status |
| `QueryReviewRuns(t, repoID, mrNumber)` | You need to inspect all runs for a repo+MR pair |

### Review status notes

- `draft` and `skipped` DB statuses map to `REVIEW_STATUS_UNSPECIFIED` in the proto API — use `WaitForReviewRun` for these
- Webhook-triggered reviews create **one** DB run: the webhook handler creates the run with the invocation ID, and the PRReview worker updates it as it progresses through the pipeline. `WaitForReviewRun` finds the worker's run by status.
- `llm.Reset()` clears both requests and `ResponseFunc`. Always set `ResponseFunc` **after** the initial `llm.Reset()` call in test setup.
- Test 9 (`TestDuplicateDiffDedup`) completes in ~2s. The debounce timer only fires for cancelled invocations; after a normal completion it is skipped.

## Current test cases (28 tests)

| Test | Description |
|---|---|
| `TestFullPipelineViaTriggerReview` | End-to-end via API trigger. Asserts LLM receives `read_file` and `search_codebase` tools. |
| `TestFullPipelineViaWebhook` | End-to-end via webhook MR event. |
| `TestInvalidWebhookSecret` | Webhook with wrong token returns 401. |
| `TestUnknownRepoWebhook` | Webhook for unknown project returns 200 (no dispatch). |
| `TestDraftMRNoReview` | Draft MR creates `status=draft` row, no Restate invocation. |
| `TestDraftToReadyTransition` | Draft → ready webhook triggers review. |
| `TestReviewDisabledRepo` | Webhook for disabled repo is ignored. |
| `TestLargeDiffShortCircuit` | Diff over 5000 lines posts "too large" comment. |
| `TestDuplicateDiffDedup` | Same HeadSHA → second review is skipped. |
| `TestProviderDeletionCascade` | Soft-deleted provider's repos are not returned. |
| `TestLLMTerminalError` | LLM 4xx → review fails (non-retryable). |
| `TestGitLab404ForMR` | GitLab 404 for MR → review fails. |
| `TestReadFileToolGracefulDegradation` | LLM calls `read_file` without repo context → gets error → review still completes. |
| `TestSearchCodebaseToolGracefulDegradation` | LLM calls `search_codebase` without collection → gets error → review completes. |
| `TestSemanticSearch` | Full pipeline with SyncRepo + IndexRepo + search-MCP wired. |
| `TestRepoSyncerCloneFailure` | SyncRepo fails → review marked failed. |
| `TestIndexerFailureGracefulDegradation` | IndexRepo fails → review proceeds without search. |
| `TestReadFileToolWorksWithSyncedRepo` | File reader tool works against a synced bare clone. |
| `TestDisableReviewStopsWebhook` | Enable → review → disable → second webhook ignored (spec A). |
| `TestReReviewOnNewPush` | Same MR, different SHA → two completed reviews (spec B). |
| `TestCancelOnNewPush` | Second webhook while review in-flight → only second completes after debounce (spec C). |
| `TestConcurrentMRReviews` | Two different MRs reviewed independently in parallel (spec D). |
| `TestZeroInlineComments` | Clean diff → summary posted, no discussions (spec F). |
| `TestInvalidTokenAtReviewTime` | GitLab 401 at review time → FAILED (spec G). |
| `TestClosedMergedMRIgnored` | action=close/merge → no review run (spec H). |
| `TestDebounceRapidPushes` | Two rapid webhooks, different SHAs → one review after debounce (spec I). |
| `TestSingleRunPerWebhookReview` | Webhook-triggered review creates exactly ONE run with invocation ID set (regression test). |
| `TestMalformedWebhookBody` | Invalid JSON body → 4xx or 200, no review run (spec M). |
| `TestManyInlineComments` | 50 inline comments all posted to GitLab (spec N). |

**Note:** Tests C and I (`TestCancelOnNewPush`, `TestDebounceRapidPushes`) trigger the debounce (configured via `DEBOUNCE_TIMEOUT=5s` in e2e). They complete in seconds instead of minutes. The suite timeout is 300s.
