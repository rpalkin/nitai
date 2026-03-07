# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Python Restate service that performs LLM-based code review. Receives a diff + MR metadata via Restate, runs a Pydantic AI agent with tools (file reader + semantic search), and returns structured review output (summary + inline comments).

## Commands

```bash
# Install dependencies (Python >=3.12)
pip install -e .

# Run locally (requires OPENROUTER_API_KEY in env)
python -m reviewer.service

# Run via Docker (from repo root)
docker compose up reviewer
```

```bash
# Run unit tests (29 tests covering tools, validation, deps)
pip install -e ".[test]"
pytest tests/

# Run a single test
pytest tests/test_tools.py -k test_read_file_success
```

## Environment Variables

- `OPENROUTER_API_KEY` — API key for OpenRouter (required)
- `REVIEW_MODEL` — OpenRouter model identifier (default: `anthropic/claude-sonnet-4-20250514`)
- `MAX_TOKENS` — max output tokens for the LLM (default: `16384`)
- `REVIEWER_HOST` — bind host (default: `0.0.0.0`)
- `REVIEWER_PORT` — bind port (default: `9090`)
- `SEARCH_MCP_URL` — search-MCP server URL for semantic search tool (default: `http://search-mcp:8080/mcp`)
- `LOG_LEVEL` — logging level (default: `INFO`, set to `DEBUG` for verbose output)

## Architecture

**Module:** `reviewer` (Python 3.12, dependencies: `restate-sdk`, `pydantic-ai[openai]`, `pydantic`, `hypercorn`, `fastmcp>=2.2.0`)

### Files

- **`service.py`** — Restate service `Reviewer` with handler `RunReview`. Receives `ReviewRequest`, builds prompt, runs Pydantic AI agent with tools, returns `ReviewResponse`. LLM call wrapped in `ctx.run("run-review-agent", ...)` for durability — Restate tracks the call, caches result for replay, and can cancel it properly. Agent run has a 5-minute `asyncio.wait_for` timeout. 4xx LLM errors are raised as `restate.TerminalError` (non-retryable). Configurable logging via `LOG_LEVEL` env var. Runs on Hypercorn ASGI server.
- **`agent.py`** — Pydantic AI `Agent[ReviewDeps, ReviewResponse]` with `output_type=ReviewResponse`. Uses `OpenAIChatModel` + `OpenAIProvider` pointed at OpenRouter (`https://openrouter.ai/api/v1`). System prompt defines reviewer persona and guidelines. Two tools registered: `read_file` and `search_codebase`.
- **`tools.py`** — Tool implementations:
  - `read_file(ctx, file_path)` — reads file from bare clone via `git --git-dir show <sha>:<path>`. Validates SHA (hex), file path (no `..` traversal). 500KB size limit + binary detection. `repo_path` and `sha` injected from `ctx.deps` (ReviewDeps).
  - `search_codebase(ctx, query)` — semantic search via search-MCP. Uses `fastmcp.Client` over HTTP to call the search-MCP container. Collection name from `ctx.deps.search_collection`.
  - `ReviewDeps` dataclass — holds `repo_path`, `target_branch_sha`, `search_collection`. All optional; tools degrade gracefully when fields are empty.
- **`prompt.py`** — `build_user_prompt(req)` — constructs the user prompt from MR metadata (title, description, author, branches, changed files) + full diff.
- **`models.py`** — Pydantic models:
  - `ReviewRequest` — diff, mr_title, mr_description, mr_author, source_branch, target_branch, changed_files, repo_path (optional), target_branch_sha (optional), search_collection (optional)
  - `ReviewResponse` — summary (str), comments (list of `ReviewComment`)
  - `ReviewComment` — file_path, line_start, line_end, body (supports multi-line ranges)
- **`tests/test_tools.py`** — 29 unit tests covering `_validate_sha`, `_validate_file_path`, `read_file_from_repo`, `search_mcp`, `ReviewDeps`

### Key Design Decisions

- **Pydantic AI from day 1** — structured output parsing, validation, retries handled automatically. Ready for tools (search, file reader) in Phase 2.
- **No `openai:` prefix on model name** — agent uses explicit `OpenAIChatModel` + `OpenAIProvider`, so model name is the OpenRouter identifier directly.
- **`openai_supports_tool_choice_required=False`** — OpenRouter doesn't support required tool choice; this profile flag disables it to avoid 400 errors.
- **Line ranges** — `ReviewComment` has `line_start` and `line_end` instead of a single `line`, supporting multi-line inline comments.
- **4xx → TerminalError** — LLM 4xx errors (auth, rate limit) are wrapped as `restate.TerminalError` so Restate won't retry them.
- **ReviewDeps pattern** — Pydantic AI deps injection. `ReviewDeps(repo_path, target_branch_sha, search_collection)` passed via `Agent[ReviewDeps, ...]`. LLM only sees `file_path`/`query` parameters; context comes from `ctx.deps`.
- **Graceful tool degradation** — `repo_path`, `target_branch_sha`, `search_collection` are optional on `ReviewRequest`. When absent, tools return "repository context not available" / "search not available" — reviews still complete without tools.
- **`asyncio.to_thread`** — `read_file` wraps sync `subprocess.run` to avoid blocking the async event loop.
- **`fastmcp.Client`** — handles full MCP protocol (initialize + initialized notification + tools/call) for search; no manual session management.
- **Error strings, not exceptions** — tools return human-readable error strings on failure so the LLM can adjust rather than crashing.
- **ctx.run() wrapper** — LLM agent call is wrapped in `ctx.run()` so Restate tracks it as a durable step. Result is cached for replay; cancellation propagates cleanly.
- **File read cache** — `ReviewDeps._file_cache` dict caches successful `read_file` results within a single review. Errors are not cached.
- **Search timeout** — `search_mcp()` has a 10-second `asyncio.wait_for` timeout on `call_tool`.
- **`.git/` path rejection** — `_validate_file_path` rejects `.git` in path components (not just `..`).
