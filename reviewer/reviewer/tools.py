import re
import subprocess
from dataclasses import dataclass


@dataclass
class ReviewDeps:
    repo_path: str | None
    target_branch_sha: str | None


_SHA_RE = re.compile(r"^[0-9a-f]{40}$")
_MAX_BYTES = 500 * 1024  # 500KB


def _validate_sha(sha: str) -> bool:
    return bool(_SHA_RE.match(sha))


def _validate_file_path(path: str) -> bool:
    if not path or "\x00" in path or path.startswith("/"):
        return False
    parts = path.replace("\\", "/").split("/")
    return ".." not in parts


def read_file_from_repo(repo_path: str, sha: str, file_path: str) -> str:
    """Read a file from a bare git repo at a specific commit SHA.

    Returns file content as a string, or a human-readable error message.
    """
    if not _validate_sha(sha):
        return f"Error: invalid SHA '{sha}'"
    if not _validate_file_path(file_path):
        return f"Error: invalid file path '{file_path}'"

    try:
        result = subprocess.run(
            ["git", "--git-dir", repo_path, "show", f"{sha}:{file_path}"],
            capture_output=True,
            timeout=30,
        )
    except subprocess.TimeoutExpired:
        return "Error: git show timed out"
    except FileNotFoundError:
        return "Error: git not found"

    if result.returncode != 0:
        stderr = result.stderr.decode("utf-8", errors="replace").strip()
        return f"Error: {stderr or 'git show failed'}"

    raw = result.stdout
    if len(raw) > _MAX_BYTES:
        return f"Error: file too large ({len(raw)} bytes, limit {_MAX_BYTES})"

    try:
        return raw.decode("utf-8")
    except UnicodeDecodeError:
        return "Error: file appears to be binary"
