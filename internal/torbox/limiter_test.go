package torbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/torbox"
)

func TestTokenBucket_Capacity(t *testing.T) {
	bucket := torbox.NewTokenBucket(3, 1*time.Minute)
	t.Cleanup(bucket.Stop)
	ctx := context.Background()

	// Should be able to consume capacity tokens without blocking
	for i := range 3 {
		if err := bucket.Wait(ctx); err != nil {
			t.Fatalf("Wait[%d]: %v", i, err)
		}
	}
}

func TestTokenBucket_BlocksWhenEmpty(t *testing.T) {
	bucket := torbox.NewTokenBucket(1, 1*time.Hour) // very slow refill
	t.Cleanup(bucket.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Consume the single token
	if err := bucket.Wait(ctx); err != nil {
		t.Fatalf("first Wait: %v", err)
	}

	// Second wait should block and eventually hit context deadline
	err := bucket.Wait(ctx)
	if err == nil {
		t.Error("expected context error when bucket is empty")
	}
}

func TestTokenBucket_NilSafe(t *testing.T) {
	// Zero/negative capacity returns a no-op bucket
	bucket := torbox.NewTokenBucket(0, 1*time.Second)
	t.Cleanup(bucket.Stop)
	if err := bucket.Wait(context.Background()); err != nil {
		t.Errorf("nil bucket Wait should succeed, got: %v", err)
	}
}

func TestMultiLimiter(t *testing.T) {
	b1 := torbox.NewTokenBucket(2, 1*time.Minute)
	b2 := torbox.NewTokenBucket(2, 1*time.Minute)
	t.Cleanup(b1.Stop)
	t.Cleanup(b2.Stop)
	ml := torbox.NewMultiLimiter(b1, b2)

	ctx := context.Background()

	// Both have capacity 2, so 2 calls should succeed
	for i := range 2 {
		if err := ml.Wait(ctx); err != nil {
			t.Fatalf("MultiLimiter Wait[%d]: %v", i, err)
		}
	}

	// Third should block on both (both exhausted)
	ctx2, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := ml.Wait(ctx2); err == nil {
		t.Error("expected context error when both limiters exhausted")
	}
}

func TestMultiLimiter_NilSafe(t *testing.T) {
	var ml *torbox.MultiLimiter
	if err := ml.Wait(context.Background()); err != nil {
		t.Errorf("nil MultiLimiter Wait should succeed, got: %v", err)
	}
}

func TestMultiLimiter_NilWaiter(t *testing.T) {
	ml := torbox.NewMultiLimiter(nil, nil)
	if err := ml.Wait(context.Background()); err != nil {
		t.Errorf("MultiLimiter with nil waiters should succeed, got: %v", err)
	}
}
