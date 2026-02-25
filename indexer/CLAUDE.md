# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Python CLI tool that indexes Git repositories into a Qdrant vector database for semantic search. Part of the larger `ai-reviewer` project. Uses OpenRouter API for OpenAI-compatible embeddings and LlamaIndex for chunking/indexing.

## Commands

```bash
# Install dependencies (requires Python >=3.11)
pip install -e .

# Run directly (requires Qdrant accessible at localhost:6333)
python main.py <git-repo-directory> [--model text-embedding-3-small] [--qdrant-url http://localhost:6333] [--recreate]

# Run via Docker Compose (from repo root)
REPO_PATH=/path/to/repo docker compose run --rm indexer

# Pass extra flags
REPO_PATH=/path/to/repo docker compose run --rm indexer /repo --recreate
```

## Environment Variables

Requires `OPENROUTER_API_KEY`, `EMBEDDING_MODEL`, and `QDRANT_URL` in the root `.env` file (see `.env.example`). The key is used to call OpenAI embedding models via OpenRouter.

## Architecture

Two modules:

- **`main.py`** — CLI entry point (Click). `path` is a positional argument (not `--path`). Walks a git repo, computes file hashes for incremental updates, connects to Qdrant, and orchestrates the index/update pipeline. `--recreate` drops and recreates the collection. Key constants: `MAX_FILE_BYTES` (500KB), `SKIP_DIRS`, `MODEL_DIMENSIONS`.
- **`splitter.py`** — Document chunking. Uses tree-sitter `CodeSplitter` for 17 programming languages (40-line chunks, 5-line overlap, 1500 char max) and falls back to `SentenceSplitter` (512 chars, 64 overlap) for other file types.

**Indexing flow:** walk files → hash-based diff against existing Qdrant collection → remove deleted files → re-chunk changed files → embed via OpenRouter → upsert nodes with hybrid indexing.

Collection names are derived from the git remote URL via `sanitize_collection_name`.

### Key Notes

- `MODEL_DIMENSIONS` dict is duplicated in both `main.py` and `search-mcp/server.py` — keep them in sync
- Untouched in Phase 1; will be adapted as a Restate Python service in Phase 2
