from pathlib import Path

from llama_index.core import Document
from llama_index.core.node_parser import CodeSplitter, SentenceSplitter
from llama_index.core.schema import TextNode

EXTENSION_TO_LANGUAGE: dict[str, str] = {
    ".py": "python",
    ".js": "javascript",
    ".jsx": "javascript",
    ".ts": "typescript",
    ".tsx": "tsx",
    ".go": "go",
    ".rs": "rust",
    ".java": "java",
    ".c": "c",
    ".h": "c",
    ".cpp": "cpp",
    ".cc": "cpp",
    ".cxx": "cpp",
    ".hpp": "cpp",
    ".cs": "c_sharp",
    ".rb": "ruby",
    ".php": "php",
    ".swift": "swift",
    ".kt": "kotlin",
    ".scala": "scala",
    ".lua": "lua",
}


def split_document(doc: Document, file_path: str) -> list[TextNode]:
    ext = Path(file_path).suffix.lower()
    language = EXTENSION_TO_LANGUAGE.get(ext)

    if language:
        try:
            splitter = CodeSplitter(
                language=language,
                chunk_lines=40,
                chunk_lines_overlap=5,
                max_chars=1500,
            )
            nodes = splitter.get_nodes_from_documents([doc])
            chunks = [n for n in nodes if n.get_content().strip()]
            if chunks:
                return chunks
        except Exception:
            pass  # fall back on any tree-sitter error

    splitter = SentenceSplitter(chunk_size=512, chunk_overlap=64)
    return [n for n in splitter.get_nodes_from_documents([doc]) if n.get_content().strip()]
