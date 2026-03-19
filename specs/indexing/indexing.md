# Indexing

The indexing subsystem maintains searchable vector embeddings of repository code using Qdrant and tree-sitter-based chunking.

## Architecture

### RepoSyncer

**Tech:** Go handler in `RepoSyncer` service
**Purpose:** Maintains a bare clone of each enabled repo on local disk; pulls latest target branch on demand
**Storage:** Local disk, path convention: `<data-dir>/<org-id>/<repo-id>/`. Object storage planned for future.
**Concurrency:** The local clone is a bare git object store. Concurrent `git fetch` and `git show` operations on bare repos are safe, so no file-level locking is needed. `SyncRepo` only runs `git fetch`; all file reads go through `git show <sha>:<path>`.

### Indexer

**Tech:** Python Restate service (existing module in this repo, adapted as handler)
**Purpose:** Walks the local repo clone, chunks code with tree-sitter, computes embeddings, upserts to Qdrant
**Collection naming:** `<repo-id>-<branch>` — one collection per repo per target branch
**Branch collection optimization:** When a PR targets a non-primary branch and no Qdrant collection exists for that branch yet, the indexer **clones the primary branch's collection** first, then re-indexes only the files that differ between the two branches. This avoids a full re-embedding for branches that share most of their code with main/master.

### Qdrant

**Purpose:** Vector database for semantic code search
**Deployment:** Docker, data persisted to local disk

### Search-MCP

**Tech:** FastMCP server (existing module in this repo)
**Purpose:** Exposes `list_collections` and `search` tools over MCP stdio, launched by the Reviewer handler as a subprocess

## Background Indexing

Primary branch indexing runs **independently** from the PR review pipeline to keep the vector index warm and avoid blocking reviews with expensive full-index operations.

**Virtual Object:** `IndexMainBranch`
**Key:** `<repo_id>`
**Handler:** `Run` (exclusive — prevents concurrent indexing of the same repo)
**Trigger:** Push to primary branch (via webhook), or self-scheduled periodic execution (configurable, default: every 6 hours).

```
+---------------------------------------------+
| Call: RepoSyncer.SyncRepo                    |
|  - pull latest primary branch               |
+----------------------+----------------------+
                        |
                        v
+---------------------------------------------+
| Call: Indexer.IndexRepo                      |
|  - incremental index of primary branch      |
|  - upsert BranchIndex record in DB          |
+----------------------+----------------------+
                        |
                        v
+---------------------------------------------+
| Schedule next run                            |
|  - ctx.send(self, delay=6h)                 |
|  - durable: survives restarts               |
+---------------------------------------------+
```

This ensures that when a PR targets the primary branch, the index is already up-to-date and the `IndexRepo` step in the PR review pipeline becomes a fast no-op.

**Self-scheduling pattern:** Restate does not have built-in cron scheduling. Instead, the handler sends a delayed invocation to itself after completing. The delay is durable — if the service restarts, Restate fires the invocation at the correct time. The initial invocation is triggered when a repo is enabled for review.

## Key Decisions

- **Bare clones at `/data/repos/<repo_id>/`** — efficient storage, safe concurrent read access
- **Collection naming: `<repo_id>_<branch>`** — one collection per repo+branch combination
- **Branch collection cloning optimization** — clone primary branch collection for feature branches, reindex only differing files
- **Self-scheduling pattern for periodic indexing** — durable delayed invocations via Restate
- **`MODEL_DIMENSIONS` dict is duplicated** in both `indexer/indexing.py` and `search-mcp/server.py` — keep them in sync