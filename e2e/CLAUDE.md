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
```

**Prerequisites:** Docker must be running. `gen/go/` must be generated (`make proto` from repo root).

## Module setup

- Standalone module: NOT part of `go.work`. Always run with `GOWORK=off`.
- Uses `replace ai-reviewer/gen => ../gen/go` in `go.mod`.
- Dependencies: `testcontainers-go` (compose), `connectrpc/connect-go`.

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
| `helpers.go` | `TestClients` (ConnectRPC), `PollReviewRun`, `SetupProviderAndRepo`, `StartStack`/`StopStack` |
| `docker-compose.e2e.yml` | Overlay: sets `OPENROUTER_BASE_URL` + `extra_hosts` to reach mock servers from containers |

## Adding test cases

See `specs/e2e-cases.md` for the full list of planned test cases (28 total).

Each test case should:
1. Configure the mock servers for the scenario (e.g., specific MR diff, draft status)
2. Trigger the action (webhook event or API call via `TestClients`)
3. Poll for completion with `PollReviewRun`
4. Assert on review run status, posted comments, or Restate invocation state
