ALTER TABLE review_comments ADD COLUMN provider_comment_id TEXT;
ALTER TABLE review_comments ADD COLUMN posted BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE review_runs ADD COLUMN summary TEXT;
