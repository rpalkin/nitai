import asyncio
import os

import restate
from hypercorn.asyncio import serve
from hypercorn.config import Config
from pydantic_ai.exceptions import ModelHTTPError

from .agent import review_agent
from .models import ReviewRequest, ReviewResponse
from .prompt import build_user_prompt

reviewer_service = restate.Service("Reviewer")


@reviewer_service.handler("RunReview")
async def run_review(ctx: restate.Context, req: ReviewRequest) -> ReviewResponse:
    try:
        result = await review_agent.run(build_user_prompt(req))
        return result.output
    except ModelHTTPError as e:
        # 4xx errors are not recoverable by retrying — mark as terminal.
        if 400 <= e.status_code < 500:
            raise restate.TerminalError(str(e), status_code=e.status_code) from e
        raise


app = restate.app([reviewer_service])


if __name__ == "__main__":
    host = os.environ.get("REVIEWER_HOST", "0.0.0.0")
    port = os.environ.get("REVIEWER_PORT", "9090")

    config = Config()
    config.bind = [f"{host}:{port}"]

    asyncio.run(serve(app, config))
