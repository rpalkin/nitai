from pydantic import BaseModel


class IndexRequest(BaseModel):
    repo_id: str
    repo_path: str                    # /data/repos/<repo_id>
    branch: str
    head_sha: str
    collection_name: str              # sanitized, e.g. <repo_id>_<branch>
    last_indexed_commit: str | None   # from branch_indexes, None = full index


class IndexResult(BaseModel):
    collection_name: str
    files_indexed: int
    chunks_upserted: int
