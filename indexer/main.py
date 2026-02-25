import hashlib
import os
import re
import subprocess
from pathlib import Path

import click
from dotenv import load_dotenv
from llama_index.core import Document, Settings, StorageContext, VectorStoreIndex
from llama_index.core.schema import TextNode
from llama_index.embeddings.openai import OpenAIEmbedding
from llama_index.vector_stores.qdrant import QdrantVectorStore
from qdrant_client import QdrantClient

from splitter import split_document

MAX_FILE_BYTES = 500 * 1024  # 500 KB
BINARY_PROBE_BYTES = 8 * 1024  # 8 KB
SKIP_DIRS = {".git", "node_modules", "__pycache__", ".venv", "dist", "build"}

MODEL_DIMENSIONS = {
    "text-embedding-3-small": 1536,
    "openai/text-embedding-3-small": 1536,
    "text-embedding-3-large": 3072,
    "openai/text-embedding-3-large": 3072,
}


def sanitize_collection_name(url: str) -> str:
    return re.sub(r"[^a-zA-Z0-9]", "_", url).strip("_")


def get_git_remote(path: str) -> str:
    result = subprocess.run(
        ["git", "remote", "get-url", "origin"],
        cwd=path,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise click.ClickException(
            f"Failed to get git remote: {result.stderr.strip()}"
        )
    return result.stdout.strip()


def is_binary(path: str) -> bool:
    try:
        with open(path, "rb") as f:
            chunk = f.read(BINARY_PROBE_BYTES)
        chunk.decode("utf-8")
        return False
    except (UnicodeDecodeError, OSError):
        return True


def hash_file(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        while chunk := f.read(65536):
            h.update(chunk)
    return h.hexdigest()


def read_file_text(path: str) -> str | None:
    try:
        with open(path, encoding="utf-8", errors="strict") as f:
            return f.read()
    except Exception:
        return None


def walk_files(project_path: str) -> list[Path]:
    result = []
    for dirpath, dirnames, filenames in os.walk(project_path):
        # Prune skip dirs and hidden dirs in-place
        dirnames[:] = [
            d for d in dirnames
            if d not in SKIP_DIRS and not d.startswith(".")
        ]
        for filename in filenames:
            if filename.startswith("."):
                continue
            fpath = Path(dirpath) / filename
            try:
                if fpath.stat().st_size > MAX_FILE_BYTES:
                    continue
            except OSError:
                continue
            if is_binary(str(fpath)):
                continue
            result.append(fpath)
    return result


def fetch_existing_hashes(qdrant_client: QdrantClient, collection_name: str) -> dict[str, str]:
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


@click.command()
@click.argument("path", type=click.Path(exists=True, file_okay=False, resolve_path=True))
@click.option("--model", default=None)
@click.option("--qdrant-url", default=None)
@click.option("--recreate", is_flag=True, default=False,
              help="Drop and recreate the collection (required when enabling hybrid for existing collections).")
def cli(path: str, model: str | None, qdrant_url: str | None, recreate: bool) -> None:
    """Index a git repository into Qdrant for semantic search."""
    load_dotenv()

    model = model or os.environ.get("EMBEDDING_MODEL", "text-embedding-3-small")
    qdrant_url = qdrant_url or os.environ.get("QDRANT_URL", "http://localhost:6333")

    api_key = os.environ.get("OPENROUTER_API_KEY")
    if not api_key:
        raise click.ClickException("OPENROUTER_API_KEY environment variable is not set")

    if model not in MODEL_DIMENSIONS:
        raise click.ClickException(
            f"Unknown model '{model}'. Supported: {', '.join(MODEL_DIMENSIONS)}"
        )

    remote_url = get_git_remote(path)
    collection_name = sanitize_collection_name(remote_url)
    click.echo(f"Collection: {collection_name}")

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

    try:
        qdrant_client = QdrantClient(url=qdrant_url)
        existing_collections = [c.name for c in qdrant_client.get_collections().collections]
    except Exception as e:
        raise click.ClickException(f"Cannot connect to Qdrant at {qdrant_url}: {e}")

    if recreate and collection_name in existing_collections:
        click.echo(f"Recreating collection '{collection_name}'...")
        qdrant_client.delete_collection(collection_name)
        existing_collections.remove(collection_name)

    vector_store = QdrantVectorStore(
        client=qdrant_client,
        collection_name=collection_name,
        enable_hybrid=True,
        batch_size=20,
    )
    storage_context = StorageContext.from_defaults(vector_store=vector_store)

    is_new = collection_name not in existing_collections

    if is_new:
        click.echo("New collection — performing full index.")
        index = VectorStoreIndex(nodes=[], storage_context=storage_context)
        existing_hashes: dict[str, str] = {}
    else:
        click.echo("Existing collection — performing incremental update.")
        index = VectorStoreIndex.from_vector_store(vector_store, storage_context=storage_context)
        existing_hashes = fetch_existing_hashes(qdrant_client, collection_name)

    all_files = walk_files(path)

    current_hashes: dict[str, str] = {}
    files_to_index: list[str] = []

    for fpath in all_files:
        rel = str(fpath.relative_to(path))
        fhash = hash_file(str(fpath))
        current_hashes[rel] = fhash
        if existing_hashes.get(rel) != fhash:
            files_to_index.append(rel)

    removed_files = set(existing_hashes.keys()) - set(current_hashes.keys())

    for rel in removed_files:
        click.echo(f"  Removing: {rel}")
        index.delete_ref_doc(rel, delete_from_docstore=True)

    changed = [r for r in files_to_index if r in existing_hashes]
    for rel in changed:
        click.echo(f"  Updating: {rel}")
        index.delete_ref_doc(rel, delete_from_docstore=True)

    all_nodes: list[TextNode] = []
    files_indexed = 0

    for fpath in all_files:
        rel = str(fpath.relative_to(path))
        if rel not in files_to_index:
            continue
        text = read_file_text(str(fpath))
        if not text:
            continue
        doc = Document(
            text=text,
            doc_id=rel,
            metadata={
                "file_path": rel,
                "file_hash": current_hashes[rel],
                "project_root": path,
            },
            excluded_embed_metadata_keys=["file_hash", "project_root", "file_path"],
        )
        nodes = split_document(doc, rel)
        if nodes:
            all_nodes.extend(nodes)
            files_indexed += 1

    if all_nodes:
        click.echo(f"Embedding and storing {len(all_nodes)} chunks from {files_indexed} files...")
        index.insert_nodes(all_nodes)

    click.echo(
        f"Done. Indexed {files_indexed} files, "
        f"{len(all_nodes)} chunks upserted, "
        f"{len(removed_files)} files removed."
    )


if __name__ == "__main__":
    cli()
