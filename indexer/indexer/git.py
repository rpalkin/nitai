import hashlib
import subprocess

MAX_FILE_BYTES = 500 * 1024  # 500 KB
SKIP_DIRS = {".git", "node_modules", "__pycache__", ".venv", "dist", "build"}


def list_files(repo_path: str, sha: str) -> list[str]:
    """List all files at the given commit SHA in a bare clone.

    Filters out hidden files/dirs and SKIP_DIRS, and files larger than MAX_FILE_BYTES.
    Uses `git ls-tree -r -l` (long format) to get file sizes in a single call.
    """
    # -l outputs lines like: <mode> <type> <object> <size>\t<path>
    result = subprocess.run(
        ["git", "--git-dir", repo_path, "ls-tree", "-r", "-l", sha],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise RuntimeError(f"git ls-tree failed: {result.stderr.strip()}")

    files = []
    for line in result.stdout.splitlines():
        # Format: "<mode> <type> <object> <size>\t<path>"
        try:
            meta, path = line.split("\t", 1)
        except ValueError:
            continue
        parts = path.split("/")
        # Skip hidden files/dirs (any component starting with '.')
        if any(p.startswith(".") for p in parts):
            continue
        # Skip SKIP_DIRS
        if any(p in SKIP_DIRS for p in parts):
            continue
        # Parse size (4th space-separated field in meta)
        try:
            size = int(meta.split()[3])
        except (IndexError, ValueError):
            continue
        if size > MAX_FILE_BYTES:
            continue
        files.append(path)

    return files


def read_file(repo_path: str, sha: str, file_path: str) -> str | None:
    """Read file content from a bare clone at the given commit SHA.

    Returns None if the file is binary or unreadable.
    """
    result = subprocess.run(
        ["git", "--git-dir", repo_path, "show", f"{sha}:{file_path}"],
        capture_output=True,
    )
    if result.returncode != 0:
        return None
    content = result.stdout
    # Detect binary: try decoding as UTF-8
    try:
        return content.decode("utf-8", errors="strict")
    except UnicodeDecodeError:
        return None


def hash_file_content(content: bytes) -> str:
    """SHA-256 of file content bytes."""
    return hashlib.sha256(content).hexdigest()


def changed_files(repo_path: str, old_sha: str, new_sha: str) -> list[str]:
    """Return list of files changed between two commits in a bare clone."""
    result = subprocess.run(
        ["git", "--git-dir", repo_path, "diff", "--name-only", f"{old_sha}..{new_sha}"],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise RuntimeError(f"git diff failed: {result.stderr.strip()}")
    return [f for f in result.stdout.splitlines() if f]
