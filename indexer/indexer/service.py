import asyncio
import os

import restate
from hypercorn.asyncio import serve
from hypercorn.config import Config

from .indexing import index_repo
from .models import IndexRequest, IndexResult

indexer_service = restate.Service("Indexer")


@indexer_service.handler("IndexRepo")
async def index_repo_handler(ctx: restate.Context, req: IndexRequest) -> IndexResult:
    qdrant_url = os.environ.get("QDRANT_URL", "http://localhost:6333")
    api_key = os.environ.get("OPENROUTER_API_KEY", "")
    model = os.environ.get("EMBEDDING_MODEL", "text-embedding-3-small")

    return await index_repo(
        repo_path=req.repo_path,
        sha=req.head_sha,
        collection_name=req.collection_name,
        last_indexed_commit=req.last_indexed_commit,
        qdrant_url=qdrant_url,
        model=model,
        api_key=api_key,
    )


app = restate.app([indexer_service])


if __name__ == "__main__":
    host = os.environ.get("INDEXER_HOST", "0.0.0.0")
    port = os.environ.get("INDEXER_PORT", "9091")

    config = Config()
    config.bind = [f"{host}:{port}"]

    asyncio.run(serve(app, config))
