import asyncio
import logging
import os

import restate
from hypercorn.asyncio import serve
from hypercorn.config import Config
from pydantic_ai.exceptions import ModelHTTPError

from .agent import review_agent
from .models import ReviewRequest, ReviewResponse
from .prompt import build_user_prompt
from .tools import ReviewDeps

AGENT_RUN_TIMEOUT = 300  # 5 minutes

# Configure logging
log_level = os.environ.get("LOG_LEVEL", "INFO").upper()
logging.basicConfig(level=getattr(logging, log_level, logging.INFO))
logger = logging.getLogger("reviewer")

reviewer_service = restate.Service("Reviewer")


@reviewer_service.handler("RunReview")
async def run_review(ctx: restate.Context, req: ReviewRequest) -> ReviewResponse:
    logger.debug("RunReview handler called")
    logger.debug(f"Request: repo_path={req.repo_path}, search_collection={req.search_collection}")
    
    deps = ReviewDeps(
        repo_path=req.repo_path,
        target_branch_sha=req.target_branch_sha,
        search_collection=req.search_collection,
    )

    async def _run_agent() -> ReviewResponse:
        """Run the review agent - wrapped in ctx.run for durability and cancellation."""
        logger.debug("Starting agent run")
        try:
            result = await asyncio.wait_for(
                review_agent.run(build_user_prompt(req), deps=deps),
                timeout=AGENT_RUN_TIMEOUT,
            )
            logger.debug(f"Agent completed with {len(result.output.comments)} comments")
            return result.output
        except asyncio.TimeoutError:
            logger.error(f"Agent run timed out after {AGENT_RUN_TIMEOUT}s")
            raise restate.TerminalError(f"review agent timed out after {AGENT_RUN_TIMEOUT}s", status_code=504)
        except ModelHTTPError as e:
            # 4xx errors are not recoverable by retrying — mark as terminal.
            logger.error(f"ModelHTTPError: status={e.status_code}, message={e}")
            if 400 <= e.status_code < 500:
                raise restate.TerminalError(str(e), status_code=e.status_code) from e
            raise
        except asyncio.CancelledError:
            logger.debug("Agent run cancelled")
            raise

    # Wrap LLM call in ctx.run() so Restate can:
    # - Track this as a durable step
    # - Record the result for replay (idempotency)
    # - Properly cancel it when the invocation is cancelled
    logger.debug("Calling ctx.run for agent execution")
    try:
        return await ctx.run("run-review-agent", _run_agent, type_hint=ReviewResponse)
    except asyncio.CancelledError:
        logger.debug("ctx.run was cancelled (invocation cancelled by Restate)")
        raise


app = restate.app([reviewer_service])


if __name__ == "__main__":
    host = os.environ.get("REVIEWER_HOST", "0.0.0.0")
    port = os.environ.get("REVIEWER_PORT", "9090")

    config = Config()
    config.bind = [f"{host}:{port}"]

    asyncio.run(serve(app, config))
