import asyncio
import logging
import os

from pydantic_ai import Agent, RunContext
from pydantic_ai.models.openai import OpenAIChatModel, OpenAIModelProfile
from pydantic_ai.providers.openai import OpenAIProvider
from pydantic_ai.settings import ModelSettings

from .models import ReviewResponse
from .prompt import SYSTEM_PROMPT
from .tools import SEARCH_MCP_URL, ReviewDeps, read_file_from_repo, search_mcp

logger = logging.getLogger("reviewer.agent")

OPENROUTER_API_KEY = os.environ["OPENROUTER_API_KEY"]
REVIEW_MODEL = os.environ.get("REVIEW_MODEL", "anthropic/claude-sonnet-4-20250514")
MAX_TOKENS = int(os.environ.get("MAX_TOKENS", "16384"))
OPENROUTER_BASE_URL = os.environ.get("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1")

review_agent: Agent[ReviewDeps, ReviewResponse] = Agent(
    model=OpenAIChatModel(
        model_name=REVIEW_MODEL,
        provider=OpenAIProvider(
            base_url=OPENROUTER_BASE_URL,
            api_key=OPENROUTER_API_KEY,
        ),
        profile=OpenAIModelProfile(openai_supports_tool_choice_required=False),
    ),
    output_type=ReviewResponse,
    instructions=SYSTEM_PROMPT,
    model_settings=ModelSettings(max_tokens=MAX_TOKENS),
)


@review_agent.tool
async def read_file(ctx: RunContext[ReviewDeps], file_path: str) -> str:
    """Read a file from the repository at the target branch HEAD."""
    logger.debug(f"read_file called: file_path={file_path}")
    if not ctx.deps.repo_path or not ctx.deps.target_branch_sha:
        logger.warning("read_file: no repo context available")
        return "Error: repository context not available for this review"

    # Check cache first
    if ctx.deps._file_cache and file_path in ctx.deps._file_cache:
        logger.debug(f"read_file: cache hit for {file_path}")
        return ctx.deps._file_cache[file_path]

    result = await asyncio.to_thread(
        read_file_from_repo, ctx.deps.repo_path, ctx.deps.target_branch_sha, file_path
    )

    # Cache the result if it's valid content (not an error)
    if ctx.deps._file_cache is not None and not result.startswith("Error:"):
        ctx.deps._file_cache[file_path] = result

    logger.debug(f"read_file: read {len(result)} bytes from {file_path}")
    return result


@review_agent.tool
async def search_codebase(ctx: RunContext[ReviewDeps], query: str) -> str:
    """Search the codebase for code semantically related to the query."""
    logger.debug(f"search_codebase called: query={query}")
    if not ctx.deps.search_collection:
        logger.warning("search_codebase: no search collection available")
        return "Error: search context not available for this review"
    results = await search_mcp(SEARCH_MCP_URL, ctx.deps.search_collection, query)
    if isinstance(results, str):
        logger.warning(f"search_codebase: error - {results}")
        return results  # error string
    if not results:
        logger.debug("search_codebase: no results found")
        return "No results found."
    logger.debug(f"search_codebase: found {len(results)} results")
    parts = []
    for r in results:
        score = r.get("score")
        score_str = f" ({score})" if score is not None else ""
        file_path = r.get("file_path", "unknown")
        content = r.get("content", "")
        parts.append(f"## {file_path}{score_str}\n```\n{content}\n```")
    return "\n\n".join(parts)
