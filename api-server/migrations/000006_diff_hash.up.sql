ALTER TYPE review_status ADD VALUE IF NOT EXISTS 'skipped';
ALTER TABLE review_runs ADD COLUMN IF NOT EXISTS diff_hash TEXT;
