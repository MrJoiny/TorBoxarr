package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"time"

	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/util"
)

const QBitSessionCookie = "SID"

type QBitSessionManager struct {
	store    *store.Store
	username string
	password string
	ttl      time.Duration
}

func NewQBitSessionManager(store *store.Store, username, password string, ttl time.Duration) *QBitSessionManager {
	return &QBitSessionManager{
		store:    store,
		username: username,
		password: password,
		ttl:      ttl,
	}
}

func (m *QBitSessionManager) Login(ctx context.Context, username, password string) (string, error) {
	if subtle.ConstantTimeCompare([]byte(username), []byte(m.username)) != 1 ||
		subtle.ConstantTimeCompare([]byte(password), []byte(m.password)) != 1 {
		return "", fmt.Errorf("invalid credentials")
	}
	sid, err := util.RandomHex(16)
	if err != nil {
		return "", err
	}
	if err := m.store.CreateQBitSession(ctx, sid, username, time.Now().UTC().Add(m.ttl)); err != nil {
		return "", err
	}
	return sid, nil
}

func (m *QBitSessionManager) Logout(ctx context.Context, sid string) error {
	return m.store.DeleteQBitSession(ctx, sid)
}

func (m *QBitSessionManager) Valid(ctx context.Context, sid string) (bool, error) {
	return m.store.ValidateQBitSession(ctx, sid)
}

func (m *QBitSessionManager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(QBitSessionCookie)
		if err != nil || cookie.Value == "" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		ok, err := m.Valid(r.Context(), cookie.Value)
		if err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if !ok {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func SetQBitCookie(w http.ResponseWriter, sid string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     QBitSessionCookie,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

func ClearQBitCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     QBitSessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}
