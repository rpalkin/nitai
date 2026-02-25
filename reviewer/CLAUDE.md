# reviewer — CLAUDE.md

Python Restate service that performs LLM-based code review. Receives a diff + MR metadata via Restate, runs a Pydantic AI agent, and returns structured review output (summary + inline comments).

## Commands

```bash
# Install dependencies (Python >=3.12)
pip install -e .

# Run locally
python -m reviewer.service

# Run via Docker (from repo root)
docker compose up reviewer
```

## Environment Variables

- `OPENROUTER_API_KEY` — API key for OpenRouter (required)
- `REVIEW_MODEL` — OpenRouter model identifier (default: `anthropic/claude-sonnet-4-20250514`)
- `REVIEWER_HOST` — bind host (default: `0.0.0.0`)
- `REVIEWER_PORT` — bind port (default: `9090`)

## Architecture

**Module:** `reviewer` (Python 3.12, dependencies: `restate-sdk`, `pydantic-ai[openai]`, `pydantic`, `hypercorn`)

### Files

- **`service.py`** — Restate service `Reviewer` with handler `RunReview`. Receives `ReviewRequest`, builds prompt, runs Pydantic AI agent, returns `ReviewResponse`. 4xx LLM errors are raised as `restate.TerminalError` (non-retryable). Runs on Hypercorn ASGI server.
- **`agent.py`** — Pydantic AI `Agent` with `result_type=ReviewResponse`. Uses `OpenAIChatModel` + `OpenAIProvider` pointed at OpenRouter (`https://openrouter.ai/api/v1`). System prompt defines reviewer persona and guidelines.
- **`prompt.py`** — `build_user_prompt(req)` — constructs the user prompt from MR metadata (title, description, author, branches, changed files) + full diff.
- **`models.py`** — Pydantic models:
  - `ReviewRequest` — diff, mr_title, mr_description, mr_author, source_branch, target_branch, changed_files
  - `ReviewResponse` — summary (str), comments (list of `ReviewComment`)
  - `ReviewComment` — file_path, line_start, line_end, body (supports multi-line ranges)

### Key Design Decisions

- **Pydantic AI from day 1** — structured output parsing, validation, retries handled automatically. Ready for tools (search, file reader) in Phase 2.
- **No `openai:` prefix on model name** — agent uses explicit `OpenAIChatModel` + `OpenAIProvider`, so model name is the OpenRouter identifier directly
- **Line ranges** — `ReviewComment` has `line_start` and `line_end` instead of a single `line`, supporting multi-line inline comments
- **No tools in Phase 1** — search-MCP and file reader deferred to Phase 2
