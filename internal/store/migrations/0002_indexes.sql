-- +goose Up
CREATE INDEX IF NOT EXISTS idx_jobs_state_next_run ON jobs(state, next_run_at);
CREATE INDEX IF NOT EXISTS idx_jobs_client_category ON jobs(client_kind, category, state);
CREATE INDEX IF NOT EXISTS idx_jobs_remote_id ON jobs(remote_id);
CREATE INDEX IF NOT EXISTS idx_jobs_updated_at ON jobs(updated_at);
CREATE INDEX IF NOT EXISTS idx_transfer_parts_job_id ON transfer_parts(job_id);
CREATE INDEX IF NOT EXISTS idx_qbit_sessions_expires_at ON qbit_sessions(expires_at);

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

-- +goose Down
DROP INDEX IF EXISTS idx_jobs_submission_key_active;
DROP INDEX IF EXISTS idx_qbit_sessions_expires_at;
DROP INDEX IF EXISTS idx_transfer_parts_job_id;
DROP INDEX IF EXISTS idx_jobs_updated_at;
DROP INDEX IF EXISTS idx_jobs_remote_id;
DROP INDEX IF EXISTS idx_jobs_client_category;
DROP INDEX IF EXISTS idx_jobs_state_next_run;
