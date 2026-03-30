-- +goose Up
ALTER TABLE jobs ADD COLUMN queued_id TEXT;
ALTER TABLE jobs ADD COLUMN queue_auth_id TEXT;
ALTER TABLE jobs ADD COLUMN remote_hash TEXT;

CREATE INDEX IF NOT EXISTS idx_jobs_queued_id ON jobs(queued_id);
CREATE INDEX IF NOT EXISTS idx_jobs_queue_auth_id ON jobs(queue_auth_id);
CREATE INDEX IF NOT EXISTS idx_jobs_remote_hash ON jobs(remote_hash);

DROP INDEX IF EXISTS idx_jobs_submission_key_active;
CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_submission_key_active
    ON jobs(submission_key)
    WHERE state IN (
        'accepted',
        'submit_pending',
        'submit_retry',
        'remote_queued',
        'remote_active',
        'local_download_pending',
        'local_downloading',
        'local_verify',
        'completed',
        'remove_pending'
    );

-- +goose Down
DROP INDEX IF EXISTS idx_jobs_submission_key_active;
DROP INDEX IF EXISTS idx_jobs_remote_hash;
DROP INDEX IF EXISTS idx_jobs_queue_auth_id;
DROP INDEX IF EXISTS idx_jobs_queued_id;
CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_submission_key_active
    ON jobs(submission_key)
    WHERE state IN (
        'accepted',
        'submit_pending',
        'submit_retry',
        'remote_active',
        'local_download_pending',
        'local_downloading',
        'local_verify',
        'completed',
        'remove_pending'
    );
