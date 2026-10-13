package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
)

func TestWorkerEnvRetainsJobsAfterSQLCancellation(t *testing.T) {
	env := newWorkerEnv(t)
	ctx := context.Background()
	job := makeWorkerJob("cancelled-sql", "pub-cancelled-sql", store.StateSubmitPending, store.SourceTypeTorrent)
	if err := env.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	var sum int64
	err := env.store.DB().QueryRowContext(queryCtx, `
        WITH RECURSIVE numbers(n) AS (
            SELECT 1 UNION ALL SELECT n + 1 FROM numbers
        ) SELECT sum(n) FROM numbers
    `).Scan(&sum)
	if err == nil || queryCtx.Err() == nil {
		t.Fatalf("query error = %v, context error = %v, want cancelled query", err, queryCtx.Err())
	}
	t.Logf("pool after cancellation: %+v", env.store.DB().Stats())
	got, err := env.store.GetJobByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.State != job.State {
		t.Fatalf("job after cancellation = %#v", got)
	}
}
