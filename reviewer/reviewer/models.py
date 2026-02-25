from pydantic import BaseModel


class ReviewRequest(BaseModel):
    diff: str
    mr_title: str
    mr_description: str
    mr_author: str
    source_branch: str
    target_branch: str
    changed_files: list[str]


class ReviewComment(BaseModel):
    file_path: str
    line_start: int
    line_end: int
    body: str


class ReviewResponse(BaseModel):
    summary: str
    comments: list[ReviewComment]
