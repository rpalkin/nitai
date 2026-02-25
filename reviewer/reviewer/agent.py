import os

from pydantic_ai import Agent
from pydantic_ai.models.openai import OpenAIChatModel, OpenAIModelProfile
from pydantic_ai.providers.openai import OpenAIProvider
from pydantic_ai.settings import ModelSettings

from .models import ReviewResponse
from .prompt import SYSTEM_PROMPT

OPENROUTER_API_KEY = os.environ["OPENROUTER_API_KEY"]
REVIEW_MODEL = os.environ.get("REVIEW_MODEL", "anthropic/claude-sonnet-4-20250514")
MAX_TOKENS = int(os.environ.get("MAX_TOKENS", "16384"))

review_agent: Agent[None, ReviewResponse] = Agent(
    model=OpenAIChatModel(
        model_name=REVIEW_MODEL,
        provider=OpenAIProvider(
            base_url="https://openrouter.ai/api/v1",
            api_key=OPENROUTER_API_KEY,
        ),
        profile=OpenAIModelProfile(openai_supports_tool_choice_required=False),
    ),
    output_type=ReviewResponse,
    instructions=SYSTEM_PROMPT,
    model_settings=ModelSettings(max_tokens=MAX_TOKENS),
)
