CREATE TABLE activity_logs (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repo_id     UUID        REFERENCES repositories(id) ON DELETE SET NULL,
    actor_id    UUID        REFERENCES users(id) ON DELETE SET NULL,
    event_type  TEXT        NOT NULL,
    details     JSONB       NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_activity_logs_org_created ON activity_logs(org_id, created_at DESC);
CREATE INDEX idx_activity_logs_event_type ON activity_logs(org_id, event_type);
CREATE INDEX idx_activity_logs_repo ON activity_logs(repo_id) WHERE repo_id IS NOT NULL;