"""Simple MCP client for testing the search-mcp server."""
import argparse
import asyncio
import os
from pathlib import Path

from dotenv import dotenv_values
from fastmcp import Client
from fastmcp.client.transports import PythonStdioTransport

_here = Path(__file__).parent
_env = {k: v for k, v in dotenv_values(_here / ".env").items() if v}
for _key in ("OPENROUTER_API_KEY", "QDRANT_URL", "EMBEDDING_MODEL"):
    if _key in os.environ:
        _env[_key] = os.environ[_key]

_transport = PythonStdioTransport(script_path=_here / "server.py", env=_env)


async def cmd_list() -> None:
    async with Client(_transport) as client:
        collections = (await client.call_tool("list_collections", {})).structured_content["result"]
        if not collections:
            print("No collections found — index a repo first.")
        else:
            for c in collections:
                print(c)


async def cmd_search(collection: str, query: str, top_k: int) -> None:
    async with Client(_transport) as client:
        results = (await client.call_tool("search", {
            "query": query,
            "collection": collection,
            "top_k": top_k,
        })).structured_content["result"]

        for r in results:
            print(f"[{r['score']}] {r['file_path']}")
            print(f"  {r['content'][:120].strip()}")
            print()


def main() -> None:
    parser = argparse.ArgumentParser(description="search-mcp client")
    sub = parser.add_subparsers(dest="cmd", required=True)

    sub.add_parser("list", help="List available collections")

    s = sub.add_parser("search", help="Search a collection")
    s.add_argument("collection", help="Collection name")
    s.add_argument("query", help="Search query")
    s.add_argument("--top-k", type=int, default=5, metavar="N", help="Number of results (default: 5)")

    args = parser.parse_args()

    if args.cmd == "list":
        asyncio.run(cmd_list())
    else:
        asyncio.run(cmd_search(args.collection, args.query, args.top_k))


if __name__ == "__main__":
    main()
