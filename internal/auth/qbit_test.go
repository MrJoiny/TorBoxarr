package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mrjoiny/torboxarr/internal/auth"
	"github.com/mrjoiny/torboxarr/internal/store"
)

func newTestQBitManager(t *testing.T) *auth.QBitSessionManager {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, ":memory:", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := store.RunMigrationsFS(db, store.EmbeddedMigrations); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db)
	return auth.NewQBitSessionManager(st, "admin", "password", 1*time.Hour)
}

func TestQBitLogin_Success(t *testing.T) {
	m := newTestQBitManager(t)
	ctx := context.Background()

	sid, err := m.Login(ctx, "admin", "password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if sid == "" {
		t.Error("expected non-empty SID")
	}
}

func TestQBitLogin_BadPassword(t *testing.T) {
	m := newTestQBitManager(t)
	ctx := context.Background()

	_, err := m.Login(ctx, "admin", "wrong")
	if err == nil {
		t.Fatal("expected error for bad password")
	}
}

func TestQBitSession_Valid(t *testing.T) {
	m := newTestQBitManager(t)
	ctx := context.Background()

	sid, err := m.Login(ctx, "admin", "password")
	if err != nil {
		t.Fatal(err)
	}

	valid, err := m.Valid(ctx, sid)
	if err != nil {
		t.Fatalf("Valid: %v", err)
	}
	if !valid {
		t.Error("expected session to be valid")
	}
}

func TestQBitLogout(t *testing.T) {
	m := newTestQBitManager(t)
	ctx := context.Background()

	sid, err := m.Login(ctx, "admin", "password")
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Logout(ctx, sid); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	valid, err := m.Valid(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if valid {
		t.Error("expected session to be invalid after logout")
	}
}

func TestQBitMiddleware_NoCookie(t *testing.T) {
	m := newTestQBitManager(t)

	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/protected", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestQBitMiddleware_ValidCookie(t *testing.T) {
	m := newTestQBitManager(t)
	ctx := context.Background()

	sid, err := m.Login(ctx, "admin", "password")
	if err != nil {
		t.Fatal(err)
	}

	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/protected", nil)
	req.AddCookie(&http.Cookie{Name: auth.QBitSessionCookie, Value: sid})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestQBitMiddleware_ExpiredCookie(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, ":memory:", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := store.RunMigrationsFS(db, store.EmbeddedMigrations); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db)

	// Create manager with very short TTL
	m := auth.NewQBitSessionManager(st, "admin", "password", -1*time.Hour)

	sid, err := m.Login(ctx, "admin", "password")
	if err != nil {
		t.Fatal(err)
	}

	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/protected", nil)
	req.AddCookie(&http.Cookie{Name: auth.QBitSessionCookie, Value: sid})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d for expired session", rec.Code, http.StatusForbidden)
	}
}
