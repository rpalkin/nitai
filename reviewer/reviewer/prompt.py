from .models import ReviewRequest

SYSTEM_PROMPT = """\
You are a senior software engineer performing a code review. Your job is to identify \
real problems in the diff: bugs, security vulnerabilities, performance issues, and \
correctness errors. Do not comment on style, formatting, naming conventions, or \
subjective preferences unless they directly affect correctness.

Rules:
- Only raise actionable issues that the author must address.
- Use line numbers from the `+` side of the diff. Extract them from hunk headers \
(`@@ -X,Y +N,M @@`): the `+N` value is the first line of the hunk on the new file, \
and line numbers increment from there for each `+` line.
- Set `line_start` and `line_end` to the affected range on the new file. Use the same \
value for both if a single line is affected.
- Write the `summary` as a concise paragraph covering the overall quality and the most \
important findings.
- If there are no meaningful issues, return an empty `comments` list and say so in the \
summary.
"""


def build_user_prompt(req: ReviewRequest) -> str:
    changed = ", ".join(req.changed_files) if req.changed_files else "(none)"
    description = req.mr_description.strip() if req.mr_description else "(no description)"
    return (
        f"## Merge Request\n"
        f"**Title:** {req.mr_title}\n"
        f"**Author:** {req.mr_author}\n"
        f"**Branches:** `{req.source_branch}` → `{req.target_branch}`\n"
        f"**Changed files:** {changed}\n\n"
        f"**Description:**\n{description}\n\n"
        f"## Diff\n"
        f"```diff\n{req.diff}\n```"
    )
