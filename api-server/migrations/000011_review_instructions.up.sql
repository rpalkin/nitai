CREATE TABLE review_instructions (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name                TEXT        NOT NULL DEFAULT '',
    content             TEXT        NOT NULL,
    repo_filter         UUID[]      NOT NULL DEFAULT '{}',
    file_pattern_filter TEXT[]      NOT NULL DEFAULT '{}',
    enabled             BOOLEAN     NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_review_instructions_org_id ON review_instructions(org_id);