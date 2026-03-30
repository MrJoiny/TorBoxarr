package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/pressly/goose/v3"
)

// gooseMu serialises RunMigrationsFS calls so that the global
// goose.SetBaseFS / goose.SetBaseFS(nil) pair cannot race across
// parallel test packages.
var gooseMu sync.Mutex

type Store struct {
	db  *sql.DB
	now func() time.Time
}

const jobColumns = `
        id, public_id, source_type, client_kind, category, state, submission_key,
        remote_id, queued_id, queue_auth_id, remote_hash, display_name, info_hash,
        source_uri, payload_ref, staging_path, completed_path, bytes_total, bytes_done,
        error_message, retry_count, next_run_at, last_remote_status, metadata_json,
        delete_requested, claimed_by, claimed_at, created_at, updated_at
`

func Open(ctx context.Context, path string, busyTimeout time.Duration) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		fmt.Sprintf("PRAGMA busy_timeout = %d;", busyTimeout.Milliseconds()),
		"PRAGMA foreign_keys = ON;",
		"PRAGMA synchronous = NORMAL;",
	}
	for _, pragma := range pragmas {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("apply pragma %q: %w", pragma, err)
		}
	}

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return db, nil
}

func RunMigrations(db *sql.DB, dir string) error {
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(db, dir); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

func RunMigrationsFS(db *sql.DB, migrationFS fs.FS) error {
	gooseMu.Lock()
	defer gooseMu.Unlock()

	goose.SetBaseFS(migrationFS)
	defer goose.SetBaseFS(nil)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		return fmt.Errorf("run embedded migrations: %w", err)
	}
	return nil
}

func New(db *sql.DB) *Store {
	return &Store{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the clock function used by the store. For testing only.
func (s *Store) SetClock(fn func() time.Time) {
	s.now = fn
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) CreateJob(ctx context.Context, job *Job) error {
	metadataJSON, err := json.Marshal(job.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
        INSERT INTO jobs (
            id, public_id, source_type, client_kind, category, state, submission_key,
            remote_id, queued_id, queue_auth_id, remote_hash, display_name, info_hash, source_uri, payload_ref, staging_path,
            completed_path, bytes_total, bytes_done, error_message, retry_count,
            next_run_at, last_remote_status, metadata_json, delete_requested,
            created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `,
		job.ID,
		job.PublicID,
		string(job.SourceType),
		string(job.ClientKind),
		job.Category,
		string(job.State),
		job.SubmissionKey,
		nullableString(job.RemoteID),
		nullableString(job.QueuedID),
		nullableString(job.QueueAuthID),
		nullableString(job.RemoteHash),
		job.DisplayName,
		nullableString(job.InfoHash),
		nullableString(job.SourceURI),
		nullableString(job.PayloadRef),
		nullableString(job.StagingPath),
		nullableString(job.CompletedPath),
		job.BytesTotal,
		job.BytesDone,
		nullableString(job.ErrorMessage),
		job.RetryCount,
		nullableTime(job.NextRunAt),
		nullableString(job.LastRemoteStatus),
		string(metadataJSON),
		boolToInt(job.DeleteRequested),
		formatTime(job.CreatedAt),
		formatTime(job.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert job: %w", err)
	}
	return s.AppendEvent(ctx, job.ID, nil, &job.State, "job created")
}

func (s *Store) UpdateJob(ctx context.Context, job *Job) error {
	metadataJSON, err := json.Marshal(job.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
        UPDATE jobs
        SET source_type = ?,
            client_kind = ?,
            category = ?,
            state = ?,
            submission_key = ?,
            remote_id = ?,
            queued_id = ?,
            queue_auth_id = ?,
            remote_hash = ?,
            display_name = ?,
            info_hash = ?,
            source_uri = ?,
            payload_ref = ?,
            staging_path = ?,
            completed_path = ?,
            bytes_total = ?,
            bytes_done = ?,
            error_message = ?,
            retry_count = ?,
            next_run_at = ?,
            last_remote_status = ?,
            metadata_json = ?,
            delete_requested = ?,
            claimed_by = NULL,
            claimed_at = NULL,
            updated_at = ?
        WHERE id = ?
    `,
		string(job.SourceType),
		string(job.ClientKind),
		job.Category,
		string(job.State),
		job.SubmissionKey,
		nullableString(job.RemoteID),
		nullableString(job.QueuedID),
		nullableString(job.QueueAuthID),
		nullableString(job.RemoteHash),
		job.DisplayName,
		nullableString(job.InfoHash),
		nullableString(job.SourceURI),
		nullableString(job.PayloadRef),
		nullableString(job.StagingPath),
		nullableString(job.CompletedPath),
		job.BytesTotal,
		job.BytesDone,
		nullableString(job.ErrorMessage),
		job.RetryCount,
		nullableTime(job.NextRunAt),
		nullableString(job.LastRemoteStatus),
		string(metadataJSON),
		boolToInt(job.DeleteRequested),
		formatTime(job.UpdatedAt),
		job.ID,
	)
	if err != nil {
		return fmt.Errorf("update job: %w", err)
	}
	return nil
}

func (s *Store) UpdateJobState(ctx context.Context, job *Job, next JobState, message string) error {
	prev := job.State
	job.State = next
	job.UpdatedAt = s.now()
	if err := s.UpdateJob(ctx, job); err != nil {
		return err
	}
	return s.AppendEvent(ctx, job.ID, &prev, &next, message)
}

func (s *Store) AppendEvent(ctx context.Context, jobID string, from, to *JobState, message string) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO job_events (job_id, from_state, to_state, message, created_at)
        VALUES (?, ?, ?, ?, ?)
    `,
		jobID,
		nullableState(from),
		nullableState(to),
		message,
		formatTime(s.now()),
	)
	if err != nil {
		return fmt.Errorf("append job event: %w", err)
	}
	return nil
}

func (s *Store) FindActiveBySubmissionKey(ctx context.Context, submissionKey string) (*Job, error) {
	row := s.db.QueryRowContext(ctx, `
        SELECT `+jobColumns+` FROM jobs
        WHERE submission_key = ?
          AND state IN ('accepted','submit_pending','submit_retry','remote_queued','remote_active','local_download_pending','local_downloading','local_verify','completed','remove_pending')
        LIMIT 1
    `, submissionKey)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (s *Store) GetJobByID(ctx context.Context, id string) (*Job, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ? LIMIT 1`, id)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return job, err
}

func (s *Store) GetJobByPublicID(ctx context.Context, publicID string) (*Job, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE public_id = ? LIMIT 1`, publicID)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return job, err
}

func (s *Store) ListVisibleClientJobs(ctx context.Context, clientKind ClientKind, category string, limit int) ([]*Job, error) {
	query := `
        SELECT ` + jobColumns + ` FROM jobs
        WHERE client_kind = ?
          AND state != 'removed'
    `
	args := []any{string(clientKind)}
	if category != "" {
		query += ` AND category = ?`
		args = append(args, category)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list visible jobs: %w", err)
	}
	defer rows.Close()
	return scanJobs(rows)
}

func (s *Store) ListJobsDue(ctx context.Context, states []JobState, now time.Time, limit int) ([]*Job, error) {
	if len(states) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(states)), ",")
	args := make([]any, 0, len(states)+2)
	for _, state := range states {
		args = append(args, string(state))
	}
	args = append(args, formatTime(now), limit)

	query := fmt.Sprintf(`
        SELECT %s
        FROM jobs
        WHERE state IN (%s)
          AND (next_run_at IS NULL OR next_run_at <= ?)
        ORDER BY created_at ASC
        LIMIT ?
    `, jobColumns, placeholders)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list due jobs: %w", err)
	}
	defer rows.Close()
	return scanJobs(rows)
}

func (s *Store) ListOpenJobs(ctx context.Context) ([]*Job, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT `+jobColumns+`
        FROM jobs
        WHERE state NOT IN ('remote_failed', 'removed', 'failed')
        ORDER BY created_at ASC
    `)
	if err != nil {
		return nil, fmt.Errorf("list open jobs: %w", err)
	}
	defer rows.Close()
	return scanJobs(rows)
}

// ClaimJobsDue atomically selects due jobs and marks them as claimed by a worker.
// Only unclaimed jobs (claimed_by IS NULL) are selected, preventing concurrent processing.
func (s *Store) ClaimJobsDue(ctx context.Context, workerID string, states []JobState, now time.Time, limit int) ([]*Job, error) {
	if len(states) == 0 {
		return nil, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(states)), ",")
	args := make([]any, 0, len(states)+2)
	for _, state := range states {
		args = append(args, string(state))
	}
	args = append(args, formatTime(now), limit)

	// Select IDs of due, unclaimed jobs
	selectQuery := fmt.Sprintf(`
		SELECT id FROM jobs
		WHERE state IN (%s)
		  AND (next_run_at IS NULL OR next_run_at <= ?)
		  AND claimed_by IS NULL
		ORDER BY created_at ASC
		LIMIT ?
	`, placeholders)

	rows, err := tx.QueryContext(ctx, selectQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("select due jobs: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// Claim them
	idPlaceholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	claimArgs := make([]any, 0, len(ids)+2)
	claimArgs = append(claimArgs, workerID, formatTime(now))
	for _, id := range ids {
		claimArgs = append(claimArgs, id)
	}
	claimQuery := fmt.Sprintf(`
		UPDATE jobs SET claimed_by = ?, claimed_at = ?
		WHERE id IN (%s)
	`, idPlaceholders)
	if _, err := tx.ExecContext(ctx, claimQuery, claimArgs...); err != nil {
		return nil, fmt.Errorf("claim jobs: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}

	// Now fetch the full job objects
	return s.GetJobsByIDs(ctx, ids)
}

// ReleaseJobClaim clears the claimed_by field after processing.
func (s *Store) ReleaseJobClaim(ctx context.Context, jobID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET claimed_by = NULL, claimed_at = NULL WHERE id = ?
	`, jobID)
	return err
}

// ReleaseAllClaims clears all outstanding claims. Used on startup to recover
// from crashes that left jobs claimed by a previous process.
func (s *Store) ReleaseAllClaims(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET claimed_by = NULL, claimed_at = NULL WHERE claimed_by IS NOT NULL`)
	return err
}

// GetJobsByIDs fetches full job objects for the given IDs.
func (s *Store) GetJobsByIDs(ctx context.Context, ids []string) ([]*Job, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	query := fmt.Sprintf(`SELECT %s FROM jobs WHERE id IN (%s) ORDER BY created_at ASC`, jobColumns, placeholders)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get jobs by ids: %w", err)
	}
	defer rows.Close()
	return scanJobs(rows)
}

func (s *Store) DeleteRemovedOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE state = 'removed' AND updated_at < ?`, formatTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("prune removed jobs: %w", err)
	}
	affected, _ := result.RowsAffected()
	return affected, nil
}

func (s *Store) UpsertTransferPart(ctx context.Context, part *TransferPart) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO transfer_parts (
            job_id, part_key, file_id, source_url, temp_path, relative_path,
            content_length, bytes_done, etag, completed, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(job_id, part_key) DO UPDATE SET
            file_id = excluded.file_id,
            source_url = excluded.source_url,
            temp_path = excluded.temp_path,
            relative_path = excluded.relative_path,
            content_length = excluded.content_length,
            bytes_done = excluded.bytes_done,
            etag = excluded.etag,
            completed = excluded.completed,
            updated_at = excluded.updated_at
    `,
		part.JobID,
		part.PartKey,
		nullableString(part.FileID),
		part.SourceURL,
		part.TempPath,
		part.RelativePath,
		part.ContentLength,
		part.BytesDone,
		nullableString(part.ETag),
		boolToInt(part.Completed),
		formatTime(part.CreatedAt),
		formatTime(part.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert transfer part: %w", err)
	}
	return nil
}

func (s *Store) ListTransferParts(ctx context.Context, jobID string) ([]*TransferPart, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT * FROM transfer_parts WHERE job_id = ? ORDER BY id ASC`, jobID)
	if err != nil {
		return nil, fmt.Errorf("list transfer parts: %w", err)
	}
	defer rows.Close()

	var parts []*TransferPart
	for rows.Next() {
		part, err := scanTransferPart(rows)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate transfer parts: %w", err)
	}
	return parts, nil
}

func (s *Store) DeleteTransferParts(ctx context.Context, jobID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM transfer_parts WHERE job_id = ?`, jobID)
	if err != nil {
		return fmt.Errorf("delete transfer parts: %w", err)
	}
	return nil
}

func (s *Store) CreateQBitSession(ctx context.Context, sid, username string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO qbit_sessions (sid, username, expires_at, created_at)
        VALUES (?, ?, ?, ?)
    `, sid, username, formatTime(expiresAt), formatTime(s.now()))
	if err != nil {
		return fmt.Errorf("create qbit session: %w", err)
	}
	return nil
}

func (s *Store) ValidateQBitSession(ctx context.Context, sid string) (bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT expires_at FROM qbit_sessions WHERE sid = ? LIMIT 1`, sid)
	var expiresAt string
	if err := row.Scan(&expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("query session: %w", err)
	}
	parsed, err := parseTime(expiresAt)
	if err != nil {
		return false, err
	}
	return parsed.After(s.now()), nil
}

func (s *Store) DeleteQBitSession(ctx context.Context, sid string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM qbit_sessions WHERE sid = ?`, sid)
	if err != nil {
		return fmt.Errorf("delete qbit session: %w", err)
	}
	return nil
}

func (s *Store) PruneExpiredQBitSessions(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM qbit_sessions WHERE expires_at <= ?`, formatTime(s.now()))
	if err != nil {
		return 0, fmt.Errorf("prune qbit sessions: %w", err)
	}
	affected, _ := result.RowsAffected()
	return affected, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanJobs(rows *sql.Rows) ([]*Job, error) {
	var jobs []*Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}
	return jobs, nil
}

func scanJob(s scanner) (*Job, error) {
	var (
		job              Job
		sourceType       string
		clientKind       string
		state            string
		remoteID         sql.NullString
		queuedID         sql.NullString
		queueAuthID      sql.NullString
		remoteHash       sql.NullString
		infoHash         sql.NullString
		sourceURI        sql.NullString
		payloadRef       sql.NullString
		stagingPath      sql.NullString
		completedPath    sql.NullString
		errorMessage     sql.NullString
		nextRunAt        sql.NullString
		lastRemoteStatus sql.NullString
		metadataJSON     string
		deleteRequested  int
		claimedBy        sql.NullString
		claimedAt        sql.NullString
		createdAt        string
		updatedAt        string
	)

	if err := s.Scan(
		&job.ID,
		&job.PublicID,
		&sourceType,
		&clientKind,
		&job.Category,
		&state,
		&job.SubmissionKey,
		&remoteID,
		&queuedID,
		&queueAuthID,
		&remoteHash,
		&job.DisplayName,
		&infoHash,
		&sourceURI,
		&payloadRef,
		&stagingPath,
		&completedPath,
		&job.BytesTotal,
		&job.BytesDone,
		&errorMessage,
		&job.RetryCount,
		&nextRunAt,
		&lastRemoteStatus,
		&metadataJSON,
		&deleteRequested,
		&claimedBy,
		&claimedAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}

	job.SourceType = SourceType(sourceType)
	job.ClientKind = ClientKind(clientKind)
	job.State = JobState(state)
	job.RemoteID = fromNullString(remoteID)
	job.QueuedID = fromNullString(queuedID)
	job.QueueAuthID = fromNullString(queueAuthID)
	job.RemoteHash = fromNullString(remoteHash)
	job.InfoHash = fromNullString(infoHash)
	job.SourceURI = fromNullString(sourceURI)
	job.PayloadRef = fromNullString(payloadRef)
	job.StagingPath = fromNullString(stagingPath)
	job.CompletedPath = fromNullString(completedPath)
	job.ErrorMessage = fromNullString(errorMessage)
	job.LastRemoteStatus = fromNullString(lastRemoteStatus)
	job.DeleteRequested = deleteRequested == 1
	job.ClaimedBy = fromNullString(claimedBy)
	if claimedAt.Valid {
		t, err := parseTime(claimedAt.String)
		if err != nil {
			return nil, err
		}
		job.ClaimedAt = &t
	}

	if nextRunAt.Valid {
		t, err := parseTime(nextRunAt.String)
		if err != nil {
			return nil, err
		}
		job.NextRunAt = &t
	}

	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return nil, err
	}
	parsedUpdatedAt, err := parseTime(updatedAt)
	if err != nil {
		return nil, err
	}
	job.CreatedAt = parsedCreatedAt
	job.UpdatedAt = parsedUpdatedAt

	if metadataJSON != "" {
		if err := json.Unmarshal([]byte(metadataJSON), &job.Metadata); err != nil {
			return nil, fmt.Errorf("unmarshal metadata: %w", err)
		}
	}

	return &job, nil
}

func scanTransferPart(s scanner) (*TransferPart, error) {
	var (
		part      TransferPart
		fileID    sql.NullString
		etag      sql.NullString
		completed int
		createdAt string
		updatedAt string
	)
	if err := s.Scan(
		&part.ID,
		&part.JobID,
		&part.PartKey,
		&fileID,
		&part.SourceURL,
		&part.TempPath,
		&part.RelativePath,
		&part.ContentLength,
		&part.BytesDone,
		&etag,
		&completed,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	part.FileID = fromNullString(fileID)
	part.ETag = fromNullString(etag)
	part.Completed = completed == 1
	c, err := parseTime(createdAt)
	if err != nil {
		return nil, err
	}
	u, err := parseTime(updatedAt)
	if err != nil {
		return nil, err
	}
	part.CreatedAt = c
	part.UpdatedAt = u
	return &part, nil
}

func nullableString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableState(v *JobState) any {
	if v == nil {
		return nil
	}
	return string(*v)
}

func nullableTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return formatTime(*v)
}

func fromNullString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	out := v.String
	return &out
}

func parseTime(v string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", v, err)
	}
	return parsed.UTC(), nil
}

func formatTime(v time.Time) string {
	return v.UTC().Format(time.RFC3339Nano)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
