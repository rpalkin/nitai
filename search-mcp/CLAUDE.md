# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

FastMCP server exposing semantic code search tools for AI agents. Queries Qdrant vector database for indexed repository code.

## Commands

```bash
# Install dependencies (Python >=3.11)
pip install -e .

# Run as stdio server (default)
python server.py

# Test via CLI client
python client.py list
python client.py search <collection> "query text" [--top-k 5]

# Run via Docker (from repo root, exposed on host port 8081)
docker compose up search-mcp
```

## Environment Variables

- `OPENROUTER_API_KEY` — API key for OpenRouter embeddings (required)
- `EMBEDDING_MODEL` — embedding model name (default: `text-embedding-3-small`)
- `QDRANT_URL` — Qdrant connection URL (default: `http://localhost:6333`)

## Architecture

Two files:

- **`server.py`** — FastMCP server with two tools:
  - `list_collections` — enumerate indexed repositories in Qdrant
  - `search` — hybrid semantic search (returns top-k results with file path, score, and content)
  - Runs as stdio by default; in Docker Compose runs as streamable-http on port 8080 (host: 8081)
- **`client.py`** — CLI test client that launches the server via `PythonStdioTransport`

### Key Notes

- `MODEL_DIMENSIONS` dict is duplicated in both `server.py` and `indexer/main.py` — keep them in sync
- Embeddings via OpenRouter (`https://openrouter.ai/api/v1`), OpenAI-compatible API
- Host port is 8081 (not 8080) to avoid conflict with Restate ingress
- Transport is controlled by `MCP_TRANSPORT` env var: `stdio` (default) or `streamable-http` (Docker). In HTTP mode, `MCP_HOST` and `MCP_PORT` configure the listener.
- Search uses hybrid mode (dense + sparse vectors via `VectorStoreQueryMode.HYBRID`), so collections must be indexed with sparse vectors enabled (handled by the indexer's `enable_hybrid=True`)
