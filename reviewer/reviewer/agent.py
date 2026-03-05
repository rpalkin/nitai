import asyncio
import os

from pydantic_ai import Agent, RunContext
from pydantic_ai.models.openai import OpenAIChatModel, OpenAIModelProfile
from pydantic_ai.providers.openai import OpenAIProvider
from pydantic_ai.settings import ModelSettings

from .models import ReviewResponse
from .prompt import SYSTEM_PROMPT
from .tools import ReviewDeps, read_file_from_repo

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
    if not ctx.deps.repo_path or not ctx.deps.target_branch_sha:
        return "Error: repository context not available for this review"
    return await asyncio.to_thread(
        read_file_from_repo, ctx.deps.repo_path, ctx.deps.target_branch_sha, file_path
    )
