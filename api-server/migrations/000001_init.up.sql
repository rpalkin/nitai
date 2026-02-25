-- Enable pgcrypto for UUID generation
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Enums
CREATE TYPE provider_type AS ENUM (
    'gitlab_self_hosted',
    'gitlab_cloud',
    'github'
);

CREATE TYPE review_status AS ENUM (
    'pending',
    'running',
    'completed',
    'failed'
);

-- Tables
CREATE TABLE organizations (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE providers (
    id              UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID          NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    type            provider_type NOT NULL,
    name            TEXT          NOT NULL,
    base_url        TEXT          NOT NULL DEFAULT '',
    token_encrypted BYTEA         NOT NULL,
    created_at      TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE TABLE repositories (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id    UUID        NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    remote_id      TEXT        NOT NULL,
    name           TEXT        NOT NULL,
    full_path      TEXT        NOT NULL,
    review_enabled BOOLEAN     NOT NULL DEFAULT false,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider_id, remote_id)
);

CREATE TABLE review_runs (
    id         UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id    UUID          NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    mr_number  BIGINT        NOT NULL,
    status     review_status NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE TABLE review_comments (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    review_run_id UUID        NOT NULL REFERENCES review_runs(id) ON DELETE CASCADE,
    file_path     TEXT        NOT NULL,
    line_start    INT         NOT NULL,
    line_end      INT         NOT NULL,
    body          TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes on FK columns
CREATE INDEX idx_providers_org_id          ON providers(org_id);
CREATE INDEX idx_repositories_provider_id  ON repositories(provider_id);
CREATE INDEX idx_review_runs_repo_id       ON review_runs(repo_id);
CREATE INDEX idx_review_comments_run_id    ON review_comments(review_run_id);

-- Composite index for (repo, MR) lookups
CREATE INDEX idx_review_runs_repo_mr ON review_runs(repo_id, mr_number);

-- Seed: default org for MVP
INSERT INTO organizations (name) VALUES ('default');
