package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, ":memory:", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // :memory: creates a separate DB per connection
	if err := store.RunMigrationsFS(db, store.EmbeddedMigrations); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return store.New(db)
}

//go:fix inline
func strPtr(s string) *string {
	return new(s)
}

//go:fix inline
func timePtr(t time.Time) *time.Time {
	return new(t)
}

func makeJob(id, publicID string, state store.JobState) *store.Job {
	now := time.Now().UTC()
	return &store.Job{
		ID:            id,
		PublicID:      publicID,
		SourceType:    store.SourceTypeTorrent,
		ClientKind:    store.ClientKindQBit,
		Category:      "movies",
		State:         state,
		SubmissionKey: "key-" + id,
		DisplayName:   "Test " + id,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}
