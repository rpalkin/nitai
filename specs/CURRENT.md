# Plan: Subphase 2.6 — Indexer as Restate Service

## Context

The indexer currently exists as a CLI tool (`indexer/main.py`) that walks a working-tree directory via `os.walk`, chunks files with tree-sitter, embeds them, and upserts to Qdrant. It runs as a one-shot Docker container gated behind the `indexer` profile.

For Phase 2, the indexer needs to become a **persistent Restate service** that:
1. Works with **bare git clones** (no working tree) — uses `git ls-tree` / `git show` instead of filesystem reads
2. Supports **incremental indexing** via a `branch_indexes` DB table
3. Is callable from the `PRReview.Run` pipeline (after `RepoSyncer.SyncRepo`)

## Changes

### 1. Restructure indexer as a Python package

**Currently:** Two flat modules (`main.py`, `splitter.py`) with no `__init__.py`.
**Change:** Create `indexer/indexer/` package alongside existing files (keep CLI for backwards compat).

**New/modified files:**
- `indexer/indexer/__init__.py` — empty
- `indexer/indexer/service.py` — Restate service (main entrypoint)
- `indexer/indexer/indexing.py` — core indexing logic extracted from `main.py`, adapted for bare clones
- `indexer/indexer/git.py` — bare clone file operations (`git ls-tree`, `git show`, `git diff --name-only`)
- `indexer/indexer/splitter.py` — move existing `splitter.py` into package
- `indexer/indexer/models.py` — Restate handler input/output models (`IndexRequest`, `IndexResult`). Pydantic is required by `restate-sdk` for handler serialization (same pattern as `reviewer/reviewer/models.py`). No new dependency — comes transitively from `restate-sdk`.
- `indexer/pyproject.toml` — update to package structure, add `restate-sdk` and `hypercorn` deps

**Remove old files:** Delete `indexer/main.py` and `indexer/splitter.py` (top-level). All code lives in the `indexer/indexer/` package now.

### 2. Bare clone file operations (`indexer/indexer/git.py`)

Replace filesystem-dependent functions with git commands on bare repos:

```python
def list_files(repo_path: str, sha: str) -> list[str]:
    """git ls-tree -r --name-only <sha>"""
    # Filter: skip hidden files/dirs, SKIP_DIRS, files > MAX_FILE_BYTES (check via git cat-file -s)

def read_file(repo_path: str, sha: str, file_path: str) -> str | None:
    """git --git-dir=<repo_path> show <sha>:<file_path>"""
    # Returns None if binary or unreadable

def hash_file_content(content: bytes) -> str:
    """SHA-256 of file content"""

def changed_files(repo_path: str, old_sha: str, new_sha: str) -> list[str]:
    """git --git-dir=<repo_path> diff --name-only <old>..<new>"""
```

All git commands use `subprocess.run` with `--git-dir=<repo_path>` (bare clone at `/data/repos/<repo_id>/`).

### 3. Core indexing logic (`indexer/indexer/indexing.py`)

Extract and adapt the pipeline from `main.py`:

```python
async def index_repo(
    repo_path: str,
    sha: str,
    collection_name: str,
    last_indexed_commit: str | None,
    qdrant_url: str,
    model: str,
    api_key: str,
) -> IndexResult:
```

**Flow:**
1. If `last_indexed_commit` is set and equals `sha` → return early (no-op)
2. If `last_indexed_commit` is set → use `git diff --name-only` for incremental file list
3. If no `last_indexed_commit` → full index via `git ls-tree`
4. For each file: `git show` to read content, hash, chunk with `split_document`, collect nodes
5. Delete removed/changed file chunks from Qdrant, insert new chunks
6. Return `IndexResult(files_indexed=N, chunks_upserted=M, collection_name=...)`

### 4. Restate service (`indexer/indexer/service.py`)

Follow the reviewer pattern:

```python
indexer_service = restate.Service("Indexer")

@indexer_service.handler("IndexRepo")
async def index_repo_handler(ctx: restate.Context, req: IndexRequest) -> IndexResult:
    # 1. Read env (QDRANT_URL, OPENROUTER_API_KEY, EMBEDDING_MODEL)
    # 2. Call index_repo(...) with last_indexed_commit for incremental diff
    # 3. Return IndexResult (caller updates branch_indexes)
    # Note: skip-if-unchanged check is done by Go caller BEFORE invoking this

app = restate.app([indexer_service])
# Hypercorn serve on INDEXER_PORT (default 9091)
```

**Models (`indexer/indexer/models.py`):**
```python
class IndexRequest(BaseModel):
    repo_id: str
    repo_path: str                    # /data/repos/<repo_id>
    branch: str
    head_sha: str
    collection_name: str              # <repo_id>_<branch> (sanitized)
    last_indexed_commit: str | None   # from branch_indexes, None = full index

class IndexResult(BaseModel):
    collection_name: str
    files_indexed: int
    chunks_upserted: int
```

### 5. Migration `000008_branch_indexes.up.sql`

```sql
CREATE TABLE branch_indexes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id UUID NOT NULL REFERENCES repos(id),
    branch TEXT NOT NULL,
    last_indexed_commit TEXT NOT NULL,
    collection_name TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repo_id, branch)
);
```

**Decision — DB access:** Go caller (PRReview.Run) owns `branch_indexes` table access:
1. Before calling `Indexer.IndexRepo`: reads `last_indexed_commit` from `branch_indexes`
2. If `last_indexed_commit == head_sha` → skip indexing entirely (no Restate call)
3. Otherwise: passes `last_indexed_commit` in `IndexRequest` for incremental diff
4. After `IndexRepo` returns: upserts `branch_indexes` with new `head_sha`

This keeps the indexer stateless (no DB dependency) — it only needs Qdrant + git + embeddings API. The skip-if-unchanged check happens in Go before the indexer is even invoked.

### 6. Docker Compose changes

**Modify the existing `indexer` service** (remove profile gate, make persistent):

```yaml
indexer:
  build: ./indexer
  depends_on:
    qdrant:
      condition: service_started
    restate:
      condition: service_healthy
  env_file: .env
  environment:
    QDRANT_URL: http://qdrant:6333
    INDEXER_HOST: "0.0.0.0"
    INDEXER_PORT: "9091"
  expose:
    - "9091"
  volumes:
    - repos:/data/repos
```

- Remove `profiles: [indexer]` — always running
- Remove `REPO_PATH` volume mount — uses bare clones from `repos` volume
- No `DATABASE_URL` needed — indexer is stateless, Go caller handles DB
- Change entrypoint to `python -m indexer.service`
- Expose port 9091 (internal only)

**Update `restate-register`:** Add indexer registration block:
```
curl -sf -X POST "$RESTATE_ADMIN/deployments" \
  -H 'content-type: application/json' \
  -d '{"uri": "http://indexer:9091"}'
```

Add `indexer: condition: service_started` to restate-register depends_on.

**Remove CLI:** Delete `indexer/main.py` (old CLI entrypoint). All indexing goes through the Restate service now.

### 7. Dockerfile update

```dockerfile
FROM python:3.12-slim
RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY pyproject.toml .
RUN pip install --no-cache-dir .
COPY indexer/ indexer/
CMD ["python", "-m", "indexer.service"]
```

Key addition: `git` package installed (needed for `git ls-tree`, `git show`, `git diff` on bare clones).

### 8. Collection naming

The phase2-plan specifies `<repo_id>-<branch>` (sanitized). The `collection_name` will be passed in the `IndexRequest` from the Go caller (which knows repo_id and branch). Use `sanitize_collection_name(f"{repo_id}_{branch}")`.

## Files to modify/create

| File | Action |
|---|---|
| `indexer/indexer/__init__.py` | Create (empty) |
| `indexer/indexer/service.py` | Create (Restate service) |
| `indexer/indexer/indexing.py` | Create (core logic from main.py) |
| `indexer/indexer/git.py` | Create (bare clone operations) |
| `indexer/indexer/models.py` | Create (Pydantic models) |
| `indexer/indexer/splitter.py` | Create (copy from `indexer/splitter.py`) |
| `indexer/pyproject.toml` | Modify (add deps, update package structure) |
| `indexer/Dockerfile` | Modify (install git, change CMD) |
| `indexer/main.py` | Delete (CLI removed) |
| `indexer/splitter.py` | Delete (moved into package) |
| `api-server/migrations/000008_branch_indexes.up.sql` | Create |
| `api-server/migrations/000008_branch_indexes.down.sql` | Create |
| `docker-compose.yml` | Modify (indexer service + restate-register) |
| `specs/phase2-plan.md` | Modify (mark 2.6 as in-progress, then in-test) |
| `specs/phases.md` | Modify (add 2.6 to completed list) |
| `indexer/CLAUDE.md` | Modify (update architecture docs) |

## Verification

1. `docker compose up -d --build` — indexer starts as persistent service on port 9091
2. Restate registration succeeds — verify via `curl http://localhost:9070/services | jq '.services[].name'` shows `Indexer`
3. After `RepoSyncer.SyncRepo` completes for a repo, invoke `Indexer.IndexRepo` via Restate:
   ```bash
   curl -X POST http://localhost:8080/Indexer/IndexRepo \
     -H 'content-type: application/json' \
     -d '{"repo_id":"...","repo_path":"/data/repos/...","branch":"main","head_sha":"...","collection_name":"..."}'
   ```
4. Verify Qdrant collection exists: `curl http://localhost:6333/collections/<name>`
5. Re-invoke with same `head_sha` — should return `skipped: true`
6. Run `make unit` — existing Go tests still pass
7. Verify `branch_indexes` table has a row with correct `last_indexed_commit`
