-- +goose Up
CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    public_id TEXT NOT NULL UNIQUE,
    source_type TEXT NOT NULL,
    client_kind TEXT NOT NULL,
    category TEXT NOT NULL,
    state TEXT NOT NULL,
    submission_key TEXT NOT NULL,
    remote_id TEXT,
    display_name TEXT NOT NULL,
    info_hash TEXT,
    source_uri TEXT,
    payload_ref TEXT,
    staging_path TEXT,
    completed_path TEXT,
    bytes_total INTEGER NOT NULL DEFAULT 0,
    bytes_done INTEGER NOT NULL DEFAULT 0,
    error_message TEXT,
    retry_count INTEGER NOT NULL DEFAULT 0,
    next_run_at TEXT,
    last_remote_status TEXT,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    delete_requested INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS transfer_parts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id TEXT NOT NULL,
    part_key TEXT NOT NULL,
    file_id TEXT,
    source_url TEXT NOT NULL,
    temp_path TEXT NOT NULL,
    relative_path TEXT NOT NULL,
    content_length INTEGER NOT NULL DEFAULT 0,
    bytes_done INTEGER NOT NULL DEFAULT 0,
    etag TEXT,
    completed INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(job_id, part_key),
    FOREIGN KEY(job_id) REFERENCES jobs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS job_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id TEXT NOT NULL,
    from_state TEXT,
    to_state TEXT,
    message TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY(job_id) REFERENCES jobs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS qbit_sessions (
    sid TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS config_store (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS config_store;
DROP TABLE IF EXISTS qbit_sessions;
DROP TABLE IF EXISTS job_events;
DROP TABLE IF EXISTS transfer_parts;
DROP TABLE IF EXISTS jobs;
