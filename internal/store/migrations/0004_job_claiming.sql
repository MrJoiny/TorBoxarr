-- +goose Up
ALTER TABLE jobs ADD COLUMN claimed_by TEXT;
ALTER TABLE jobs ADD COLUMN claimed_at TEXT;

CREATE INDEX IF NOT EXISTS idx_jobs_claimed_by ON jobs(claimed_by);

-- +goose Down
DROP INDEX IF EXISTS idx_jobs_claimed_by;
ALTER TABLE jobs DROP COLUMN claimed_by;
ALTER TABLE jobs DROP COLUMN claimed_at;
