import subprocess

from llama_index.core import Document, Settings, StorageContext, VectorStoreIndex
from llama_index.core.schema import TextNode
from llama_index.embeddings.openai import OpenAIEmbedding
from llama_index.vector_stores.qdrant import QdrantVectorStore
from qdrant_client import QdrantClient

from .git import changed_files, hash_file_content, list_files
from .models import IndexResult
from .splitter import split_document

MODEL_DIMENSIONS = {
    "text-embedding-3-small": 1536,
    "openai/text-embedding-3-small": 1536,
    "text-embedding-3-large": 3072,
    "openai/text-embedding-3-large": 3072,
}


def _fetch_existing_hashes(qdrant_client: QdrantClient, collection_name: str) -> dict[str, str]:
    hashes: dict[str, str] = {}
    offset = None
    while True:
        response = qdrant_client.scroll(
            collection_name=collection_name,
            limit=250,
            offset=offset,
            with_payload=["file_path", "file_hash"],
            with_vectors=False,
        )
        points, next_offset = response
        for point in points:
            payload = point.payload or {}
            file_path = payload.get("file_path")
            file_hash = payload.get("file_hash")
            if file_path and file_hash:
                hashes[file_path] = file_hash
        if next_offset is None:
            break
        offset = next_offset
    return hashes


async def index_repo(
    repo_path: str,
    sha: str,
    collection_name: str,
    last_indexed_commit: str | None,
    qdrant_url: str,
    model: str,
    api_key: str,
) -> IndexResult:
    """Index a branch from a bare clone into Qdrant.

    If last_indexed_commit equals sha, returns early (no-op).
    If last_indexed_commit is set (and different), performs incremental diff.
    Otherwise performs a full index.
    """
    if last_indexed_commit is not None and last_indexed_commit == sha:
        return IndexResult(
            collection_name=collection_name,
            files_indexed=0,
            chunks_upserted=0,
        )

    if model not in MODEL_DIMENSIONS:
        raise ValueError(f"Unknown model '{model}'. Supported: {', '.join(MODEL_DIMENSIONS)}")

    Settings.embed_model = OpenAIEmbedding(
        model=model,
        dimensions=MODEL_DIMENSIONS[model],
        api_base="https://openrouter.ai/api/v1",
        api_key=api_key,
        default_headers={
            "HTTP-Referer": "https://github.com/ai-reviewer",
            "X-Title": "ai-reviewer-indexer",
        },
    )

    qdrant_client = QdrantClient(url=qdrant_url)
    existing_collections = [c.name for c in qdrant_client.get_collections().collections]

    vector_store = QdrantVectorStore(
        client=qdrant_client,
        collection_name=collection_name,
        enable_hybrid=True,
        batch_size=20,
    )
    storage_context = StorageContext.from_defaults(vector_store=vector_store)

    is_new = collection_name not in existing_collections

    if is_new:
        index = VectorStoreIndex(nodes=[], storage_context=storage_context)
        existing_hashes: dict[str, str] = {}
    else:
        index = VectorStoreIndex.from_vector_store(vector_store, storage_context=storage_context)
        existing_hashes = _fetch_existing_hashes(qdrant_client, collection_name)

    # Determine which files to process
    if last_indexed_commit is not None and not is_new:
        # Incremental: only changed files between last indexed commit and new sha
        diff_files = set(changed_files(repo_path, last_indexed_commit, sha))
        all_file_paths = list_files(repo_path, sha)
        files_to_process = [f for f in all_file_paths if f in diff_files]
        # Also handle files that were in diff but may have been deleted
        deleted_in_diff = diff_files - set(all_file_paths)
        for rel in deleted_in_diff:
            if rel in existing_hashes:
                index.delete_ref_doc(rel, delete_from_docstore=True)
    else:
        # Full index
        all_file_paths = list_files(repo_path, sha)
        files_to_process = all_file_paths
        # Remove files that no longer exist
        current_set = set(all_file_paths)
        removed_files = set(existing_hashes.keys()) - current_set
        for rel in removed_files:
            index.delete_ref_doc(rel, delete_from_docstore=True)

    # Compute current hashes for files to process
    current_hashes: dict[str, str] = {}
    files_needing_index: list[str] = []

    for rel in files_to_process:
        raw = _read_file_bytes(repo_path, sha, rel)
        if raw is None:
            continue
        fhash = hash_file_content(raw)
        current_hashes[rel] = fhash
        if existing_hashes.get(rel) != fhash:
            files_needing_index.append(rel)

    # Delete outdated chunks for files that changed
    for rel in files_needing_index:
        if rel in existing_hashes:
            index.delete_ref_doc(rel, delete_from_docstore=True)

    # Build and insert new nodes
    all_nodes: list[TextNode] = []
    files_indexed = 0

    for rel in files_needing_index:
        raw = _read_file_bytes(repo_path, sha, rel)
        if raw is None:
            continue
        try:
            text = raw.decode("utf-8", errors="strict")
        except UnicodeDecodeError:
            continue
        doc = Document(
            text=text,
            doc_id=rel,
            metadata={
                "file_path": rel,
                "file_hash": current_hashes[rel],
                "repo_path": repo_path,
            },
            excluded_embed_metadata_keys=["file_hash", "repo_path", "file_path"],
        )
        nodes = split_document(doc, rel)
        if nodes:
            all_nodes.extend(nodes)
            files_indexed += 1

    if all_nodes:
        index.insert_nodes(all_nodes)

    return IndexResult(
        collection_name=collection_name,
        files_indexed=files_indexed,
        chunks_upserted=len(all_nodes),
    )


def _read_file_bytes(repo_path: str, sha: str, file_path: str) -> bytes | None:
    """Read raw bytes of a file from a bare clone."""
    result = subprocess.run(
        ["git", "--git-dir", repo_path, "show", f"{sha}:{file_path}"],
        capture_output=True,
    )
    if result.returncode != 0:
        return None
    return result.stdout
