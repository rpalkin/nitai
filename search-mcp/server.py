import os

from dotenv import load_dotenv
from fastmcp import FastMCP
from llama_index.core import Settings, VectorStoreIndex
from llama_index.core.vector_stores.types import VectorStoreQueryMode
from llama_index.embeddings.openai import OpenAIEmbedding
from llama_index.vector_stores.qdrant import QdrantVectorStore
from qdrant_client import QdrantClient

load_dotenv()

OPENROUTER_API_KEY = os.environ.get("OPENROUTER_API_KEY", "")
QDRANT_URL = os.environ.get("QDRANT_URL", "http://localhost:6333")
EMBEDDING_MODEL = os.environ.get("EMBEDDING_MODEL", "text-embedding-3-small")

MODEL_DIMENSIONS = {
    "text-embedding-3-small": 1536,
    "openai/text-embedding-3-small": 1536,
    "text-embedding-3-large": 3072,
    "openai/text-embedding-3-large": 3072,
}

if not OPENROUTER_API_KEY:
    raise RuntimeError("OPENROUTER_API_KEY environment variable is not set")

if EMBEDDING_MODEL not in MODEL_DIMENSIONS:
    raise RuntimeError(
        f"Unknown model '{EMBEDDING_MODEL}'. Supported: {', '.join(MODEL_DIMENSIONS)}"
    )

Settings.embed_model = OpenAIEmbedding(
    model=EMBEDDING_MODEL,
    dimensions=MODEL_DIMENSIONS[EMBEDDING_MODEL],
    api_base="https://openrouter.ai/api/v1",
    api_key=OPENROUTER_API_KEY,
    default_headers={
        "HTTP-Referer": "https://github.com/ai-reviewer",
        "X-Title": "ai-reviewer-search-mcp",
    },
)

qdrant_client = QdrantClient(url=QDRANT_URL)

mcp = FastMCP(
    name="ai-reviewer-search",
    instructions=(
        "Semantic search over indexed git repositories. "
        "Use list_collections to see available repositories, "
        "then search to find relevant code chunks by natural language query."
    ),
)


@mcp.tool()
def list_collections() -> list[str]:
    """List all indexed repositories available for search."""
    return [c.name for c in qdrant_client.get_collections().collections]


@mcp.tool()
def search(query: str, collection: str, top_k: int = 5) -> list[dict]:
    """Search for code chunks semantically similar to the query.

    Args:
        query: Natural language search query.
        collection: Repository collection name (from list_collections).
        top_k: Number of results to return (max 20).
    """
    top_k = min(top_k, 20)

    available = [c.name for c in qdrant_client.get_collections().collections]
    if collection not in available:
        raise ValueError(
            f"Collection '{collection}' not found. Available: {', '.join(available)}"
        )

    vector_store = QdrantVectorStore(
        client=qdrant_client,
        collection_name=collection,
        enable_hybrid=True,
    )
    index = VectorStoreIndex.from_vector_store(vector_store)
    retriever = index.as_retriever(
        similarity_top_k=top_k,
        vector_store_query_mode=VectorStoreQueryMode.HYBRID,
        sparse_top_k=top_k * 2,
    )
    nodes = retriever.retrieve(query)

    results = []
    for node in nodes:
        metadata = node.metadata or {}
        results.append({
            "file_path": metadata.get("file_path", ""),
            "score": round(node.score, 4) if node.score is not None else None,
            "content": node.get_content(),
            "project_root": metadata.get("project_root", ""),
        })
    return results


def main() -> None:
    transport = os.environ.get("MCP_TRANSPORT", "stdio")
    kwargs = {}
    if transport != "stdio":
        kwargs["host"] = os.environ.get("MCP_HOST", "0.0.0.0")
        kwargs["port"] = int(os.environ.get("MCP_PORT", "8080"))
    mcp.run(transport=transport, **kwargs)


if __name__ == "__main__":
    main()
