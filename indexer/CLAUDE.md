# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Python Restate service that indexes Git repositories (bare clones) into a Qdrant vector database for semantic search. Part of the larger `ai-reviewer` project. Uses OpenRouter API for OpenAI-compatible embeddings and LlamaIndex for chunking/indexing.

## Commands

```bash
# Install dependencies (requires Python >=3.11)
pip install -e .

# Run locally (requires Qdrant accessible at localhost:6333)
python -m indexer.service

# Run via Docker (from repo root)
docker compose up indexer
```

## Environment Variables

- `OPENROUTER_API_KEY` — API key for OpenRouter (required)
- `EMBEDDING_MODEL` — embedding model identifier (default: `text-embedding-3-small`)
- `QDRANT_URL` — Qdrant URL (default: `http://localhost:6333`)
- `INDEXER_HOST` — bind host (default: `0.0.0.0`)
- `INDEXER_PORT` — bind port (default: `9091`)

## Architecture

**Module:** `indexer` (Python 3.12, dependencies: `restate-sdk`, `hypercorn`, llama-index, qdrant-client, tree-sitter)

### Files

- **`indexer/service.py`** — Restate service `Indexer` with handler `IndexRepo`. Receives `IndexRequest`, calls `index_repo(...)`, returns `IndexResult`. Runs on Hypercorn ASGI server on port 9091.
- **`indexer/models.py`** — Pydantic models: `IndexRequest` (repo_id, repo_path, branch, head_sha, collection_name, last_indexed_commit) and `IndexResult` (collection_name, files_indexed, chunks_upserted).
- **`indexer/indexing.py`** — Core indexing pipeline. Handles full and incremental indexing via git bare clone operations. Returns early (no-op) if `last_indexed_commit == head_sha`.
- **`indexer/git.py`** — Bare clone file operations: `list_files` (git ls-tree), `read_file` (git show), `hash_file_content`, `changed_files` (git diff --name-only). All use `--git-dir` flag for bare clone access.
- **`indexer/splitter.py`** — Document chunking. Uses tree-sitter `CodeSplitter` for 17 programming languages and falls back to `SentenceSplitter` for other file types.

### Indexing Flow

1. Go caller reads `branch_indexes.last_indexed_commit` from DB.
2. If `last_indexed_commit == head_sha` → Go caller skips the Restate call entirely.
3. Otherwise Go caller invokes `Indexer.IndexRepo` with `last_indexed_commit` (None for first-time).
4. If `last_indexed_commit` is set → incremental diff via `git diff --name-only`.
5. Otherwise → full index via `git ls-tree`.
6. Files are read via `git show <sha>:<path>` on the bare clone.
7. Files are chunked, embedded, and upserted into Qdrant.
8. Go caller upserts `branch_indexes` row with new `head_sha`.

### Key Notes

- **Stateless:** Indexer has no DB dependency. Go caller owns `branch_indexes` table access.
- **Bare clone access:** All file reads use `git --git-dir=<repo_path> show <sha>:<path>` — no working tree required.
- `MODEL_DIMENSIONS` dict is duplicated in both `indexer/indexing.py` and `search-mcp/server.py` — keep them in sync.
- Collection names are passed in from the Go caller (derived from `sanitize_collection_name(f"{repo_id}_{branch}")`).
