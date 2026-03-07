"""Unit tests for reviewer/tools.py"""
import json
import subprocess
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from reviewer.tools import (
    ReviewDeps,
    _validate_file_path,
    _validate_sha,
    read_file_from_repo,
    search_mcp,
)

VALID_SHA = "a" * 40


# ---------------------------------------------------------------------------
# _validate_sha
# ---------------------------------------------------------------------------


class TestValidateSha:
    def test_valid_sha(self):
        assert _validate_sha(VALID_SHA) is True

    def test_valid_sha_digits(self):
        assert _validate_sha("0123456789abcdef" * 2 + "01234567") is True

    def test_uppercase_rejected(self):
        assert _validate_sha("A" * 40) is False

    def test_mixed_case_rejected(self):
        assert _validate_sha("A" * 20 + "a" * 20) is False

    def test_short_sha_rejected(self):
        assert _validate_sha("a" * 39) is False

    def test_long_sha_rejected(self):
        assert _validate_sha("a" * 41) is False

    def test_empty_string_rejected(self):
        assert _validate_sha("") is False

    def test_non_hex_rejected(self):
        assert _validate_sha("g" * 40) is False


# ---------------------------------------------------------------------------
# _validate_file_path
# ---------------------------------------------------------------------------


class TestValidateFilePath:
    def test_valid_simple_path(self):
        assert _validate_file_path("src/main.go") is True

    def test_valid_nested_path(self):
        assert _validate_file_path("a/b/c/d.py") is True

    def test_valid_filename_only(self):
        assert _validate_file_path("README.md") is True

    def test_dotdot_rejected(self):
        assert _validate_file_path("../etc/passwd") is False

    def test_dotdot_nested_rejected(self):
        assert _validate_file_path("src/../../secret") is False

    def test_dotdot_with_backslash_rejected(self):
        # backslash normalised → ..
        assert _validate_file_path("src\\..\\secret") is False

    def test_absolute_path_rejected(self):
        assert _validate_file_path("/etc/passwd") is False

    def test_nul_byte_rejected(self):
        assert _validate_file_path("src/main\x00.go") is False

    def test_empty_string_rejected(self):
        assert _validate_file_path("") is False

    def test_backslash_normalised_valid(self):
        # Windows-style path without traversal is fine
        assert _validate_file_path("src\\main.go") is True


# ---------------------------------------------------------------------------
# read_file_from_repo
# ---------------------------------------------------------------------------


def _make_completed_process(stdout=b"", stderr=b"", returncode=0):
    result = MagicMock(spec=subprocess.CompletedProcess)
    result.stdout = stdout
    result.stderr = stderr
    result.returncode = returncode
    return result


class TestReadFileFromRepo:
    def test_invalid_sha_returns_error(self):
        out = read_file_from_repo("/repo.git", "bad-sha", "file.py")
        assert out.startswith("Error: invalid SHA")

    def test_invalid_path_returns_error(self):
        out = read_file_from_repo("/repo.git", VALID_SHA, "../etc/passwd")
        assert out.startswith("Error: invalid file path")

    def test_successful_read(self):
        content = b"print('hello')\n"
        with patch("reviewer.tools.subprocess.run", return_value=_make_completed_process(stdout=content)) as mock_run:
            out = read_file_from_repo("/repo.git", VALID_SHA, "hello.py")
        assert out == content.decode()
        mock_run.assert_called_once_with(
            ["git", "--git-dir", "/repo.git", "show", f"{VALID_SHA}:hello.py"],
            capture_output=True,
            timeout=30,
        )

    def test_file_not_found_returns_error(self):
        proc = _make_completed_process(stderr=b"fatal: Path not found", returncode=128)
        with patch("reviewer.tools.subprocess.run", return_value=proc):
            out = read_file_from_repo("/repo.git", VALID_SHA, "missing.py")
        assert "fatal: Path not found" in out

    def test_git_show_no_stderr_returns_generic_error(self):
        proc = _make_completed_process(stderr=b"", returncode=1)
        with patch("reviewer.tools.subprocess.run", return_value=proc):
            out = read_file_from_repo("/repo.git", VALID_SHA, "file.py")
        assert "git show failed" in out

    def test_timeout_returns_error(self):
        with patch("reviewer.tools.subprocess.run", side_effect=subprocess.TimeoutExpired("git", 30)):
            out = read_file_from_repo("/repo.git", VALID_SHA, "file.py")
        assert "timed out" in out

    def test_git_not_found_returns_error(self):
        with patch("reviewer.tools.subprocess.run", side_effect=FileNotFoundError):
            out = read_file_from_repo("/repo.git", VALID_SHA, "file.py")
        assert "git not found" in out

    def test_oversized_file_returns_error(self):
        big = b"x" * (500 * 1024 + 1)
        proc = _make_completed_process(stdout=big)
        with patch("reviewer.tools.subprocess.run", return_value=proc):
            out = read_file_from_repo("/repo.git", VALID_SHA, "big.bin")
        assert "too large" in out

    def test_binary_file_returns_error(self):
        binary = b"\xff\xfe" + b"\x00" * 100  # UTF-16 — not valid UTF-8
        proc = _make_completed_process(stdout=binary)
        with patch("reviewer.tools.subprocess.run", return_value=proc):
            out = read_file_from_repo("/repo.git", VALID_SHA, "data.bin")
        assert "binary" in out


# ---------------------------------------------------------------------------
# search_mcp
# ---------------------------------------------------------------------------


_SEARCH_RESULTS = [
    {"file_path": "src/foo.go", "score": 0.95, "content": "func Foo() {}", "project_root": "/repo"},
    {"file_path": "src/bar.go", "score": 0.80, "content": "func Bar() {}", "project_root": "/repo"},
]


def _make_text_content(text: str):
    obj = MagicMock()
    obj.text = text
    return obj


def _make_mock_client(call_tool_result=None, call_tool_side_effect=None):
    mock_client = AsyncMock()
    mock_client.__aenter__ = AsyncMock(return_value=mock_client)
    mock_client.__aexit__ = AsyncMock(return_value=False)
    if call_tool_side_effect is not None:
        mock_client.call_tool = AsyncMock(side_effect=call_tool_side_effect)
    else:
        # Wrap the content list in an object with a .content attribute
        mock_result = MagicMock()
        mock_result.content = call_tool_result
        mock_client.call_tool = AsyncMock(return_value=mock_result)
    return mock_client


@pytest.mark.asyncio
class TestSearchMcp:
    async def test_successful_search(self):
        content = [_make_text_content(json.dumps(_SEARCH_RESULTS))]
        mock_client = _make_mock_client(call_tool_result=content)

        with patch("reviewer.tools.Client", return_value=mock_client):
            result = await search_mcp("http://search-mcp:8080", "my-repo", "find Foo function")

        assert isinstance(result, list)
        assert len(result) == 2
        assert result[0]["file_path"] == "src/foo.go"
        assert result[1]["score"] == 0.80
        mock_client.call_tool.assert_awaited_once_with(
            "search", {"query": "find Foo function", "collection": "my-repo", "top_k": 5}
        )

    async def test_connection_error(self):
        mock_client = _make_mock_client(call_tool_side_effect=ConnectionError("Connection refused"))

        with patch("reviewer.tools.Client", return_value=mock_client):
            result = await search_mcp("http://search-mcp:8080", "my-repo", "query")

        assert isinstance(result, str)
        assert "Error" in result

    async def test_timeout(self):
        mock_client = _make_mock_client(call_tool_side_effect=TimeoutError("timed out"))

        with patch("reviewer.tools.Client", return_value=mock_client):
            result = await search_mcp("http://search-mcp:8080", "my-repo", "query")

        assert isinstance(result, str)
        assert "Error" in result

    async def test_empty_results(self):
        mock_client = _make_mock_client(call_tool_result=[])

        with patch("reviewer.tools.Client", return_value=mock_client):
            result = await search_mcp("http://search-mcp:8080", "my-repo", "query")

        assert result == []

    async def test_invalid_json(self):
        content = [_make_text_content("not-json{{")]
        mock_client = _make_mock_client(call_tool_result=content)

        with patch("reviewer.tools.Client", return_value=mock_client):
            result = await search_mcp("http://search-mcp:8080", "my-repo", "query")

        assert isinstance(result, str)
        assert "Error" in result

    async def test_server_error(self):
        mock_client = _make_mock_client(call_tool_side_effect=RuntimeError("server error"))

        with patch("reviewer.tools.Client", return_value=mock_client):
            result = await search_mcp("http://search-mcp:8080", "my-repo", "query")

        assert isinstance(result, str)
        assert "Error" in result


# ---------------------------------------------------------------------------
# ReviewDeps
# ---------------------------------------------------------------------------


class TestReviewDeps:
    def test_both_none(self):
        deps = ReviewDeps(repo_path=None, target_branch_sha=None)
        assert deps.repo_path is None
        assert deps.target_branch_sha is None
        assert deps.search_collection is None

    def test_both_set(self):
        deps = ReviewDeps(repo_path="/repo.git", target_branch_sha=VALID_SHA)
        assert deps.repo_path == "/repo.git"
        assert deps.target_branch_sha == VALID_SHA
        assert deps.search_collection is None

    def test_with_search_collection(self):
        deps = ReviewDeps(repo_path="/repo.git", target_branch_sha=VALID_SHA, search_collection="my-repo")
        assert deps.search_collection == "my-repo"
