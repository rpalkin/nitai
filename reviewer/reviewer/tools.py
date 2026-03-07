import asyncio
import json
import logging
import os
import re
import subprocess
from dataclasses import dataclass

from fastmcp import Client

logger = logging.getLogger("reviewer.tools")

SEARCH_TIMEOUT = 10  # seconds
MAX_SEARCH_RESPONSE_SIZE = 100 * 1024  # 100KB


@dataclass
class ReviewDeps:
    repo_path: str | None
    target_branch_sha: str | None
    search_collection: str | None = None
    _file_cache: dict[str, str] | None = None

    def __post_init__(self):
        if self._file_cache is None:
            self._file_cache = {}


SEARCH_MCP_URL = os.environ.get("SEARCH_MCP_URL", "http://search-mcp:8080")

_SHA_RE = re.compile(r"^[0-9a-f]{40}$")
_MAX_BYTES = 500 * 1024  # 500KB


def _validate_sha(sha: str) -> bool:
    return bool(_SHA_RE.match(sha))


def _validate_file_path(path: str) -> bool:
    if not path or "\x00" in path or path.startswith("/"):
        return False
    parts = path.replace("\\", "/").split("/")
    if ".." in parts or ".git" in parts:
        return False
    return True


def read_file_from_repo(repo_path: str, sha: str, file_path: str) -> str:
    """Read a file from a bare git repo at a specific commit SHA.

    Returns file content as a string, or a human-readable error message.
    """
    logger.debug(f"read_file_from_repo: repo={repo_path}, sha={sha[:8]}..., file={file_path}")
    if not _validate_sha(sha):
        logger.debug(f"read_file_from_repo: invalid SHA '{sha}'")
        return f"Error: invalid SHA '{sha}'"
    if not _validate_file_path(file_path):
        logger.debug(f"read_file_from_repo: invalid file path '{file_path}'")
        return f"Error: invalid file path '{file_path}'"

    try:
        result = subprocess.run(
            ["git", "--git-dir", repo_path, "show", f"{sha}:{file_path}"],
            capture_output=True,
            timeout=30,
        )
    except subprocess.TimeoutExpired:
        logger.warning("read_file_from_repo: git show timed out")
        return "Error: git show timed out"
    except FileNotFoundError:
        logger.warning("read_file_from_repo: git not found")
        return "Error: git not found"

    if result.returncode != 0:
        stderr = result.stderr.decode("utf-8", errors="replace").strip()
        logger.warning(f"read_file_from_repo: git show failed - {stderr}")
        return f"Error: {stderr or 'git show failed'}"

    raw = result.stdout
    if len(raw) > _MAX_BYTES:
        logger.warning(f"read_file_from_repo: file too large ({len(raw)} bytes)")
        return f"Error: file too large ({len(raw)} bytes, limit {_MAX_BYTES})"

    try:
        content = raw.decode("utf-8")
        logger.debug(f"read_file_from_repo: successfully read {len(content)} bytes")
        return content
    except UnicodeDecodeError:
        logger.warning("read_file_from_repo: file appears to be binary")
        return "Error: file appears to be binary"


async def search_mcp(url: str, collection: str, query: str, top_k: int = 5) -> list[dict] | str:
    """Call search-mcp via fastmcp Client.

    Returns a list of result dicts on success, or a human-readable error string.
    """
    logger.debug(f"search_mcp: url={url}, collection={collection}, query='{query}', top_k={top_k}")
    try:
        async with Client(f"{url}/mcp/") as client:
            result = await asyncio.wait_for(
                client.call_tool(
                    "search",
                    {"query": query, "collection": collection, "top_k": top_k},
                ),
                timeout=SEARCH_TIMEOUT,
            )
            if not result or not result.content:
                return []
            first_content = result.content[0]
            text = getattr(first_content, 'text', None)
            if text is None:
                return []
            data = json.loads(text)
            # Validate response size after parsing and truncate results if needed
            if len(text) > MAX_SEARCH_RESPONSE_SIZE:
                logger.warning(f"search_mcp: response too large ({len(text)} bytes), truncating results")
                # Truncate by reducing number of results
                data = data[:3] if len(data) > 3 else data
            logger.debug(f"search_mcp: received {len(data)} results")
            return data
    except asyncio.TimeoutError:
        logger.warning(f"search_mcp: timed out after {SEARCH_TIMEOUT}s")
        return f"Error: search-mcp timed out after {SEARCH_TIMEOUT}s"
    except Exception as e:
        logger.warning(f"search_mcp: failed with error - {e}")
        return f"Error: search-mcp failed: {e}"
