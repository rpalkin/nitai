CREATE TABLE branch_indexes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id UUID NOT NULL REFERENCES repositories(id),
    branch TEXT NOT NULL,
    last_indexed_commit TEXT NOT NULL,
    collection_name TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repo_id, branch)
);
