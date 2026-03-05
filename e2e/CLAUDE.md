# e2e — End-to-End Test Suite

Standalone Go module (`ai-reviewer/e2e`) containing full-stack tests for the ai-reviewer system.

## Build tag

All test files use `//go:build e2e`. Tests are excluded from normal `go test ./...` runs.

## Running

```bash
# From repo root (requires Docker):
make e2e

# Equivalent direct command:
cd e2e && GOWORK=off go test -v -tags e2e -count=1 -timeout 300s ./...

# Live mode (real GitLab, real LLM):
E2E_LIVE=1 GITLAB_URL=... GITLAB_TOKEN=... make e2e

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

See `specs/e2e-cases.md` for the full list of planned test cases (28 total).

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
- Webhook-triggered reviews create **two** DB runs: one by the webhook handler (holds `restate_invocation_id`), one by the PRReview worker (goes through the pipeline). `WaitForReviewRun` finds the worker's run by status.
- `llm.Reset()` clears both requests and `ResponseFunc`. Always set `ResponseFunc` **after** the initial `llm.Reset()` call in test setup.
- Test 9 (`TestDuplicateDiffDedup`) completes in ~2s. The debounce timer only fires for cancelled invocations; after a normal completion it is skipped.
