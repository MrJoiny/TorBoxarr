package api

import (
	"net/url"
	"testing"
)

func TestSanitizeQuery_RedactsSensitiveValues(t *testing.T) {
	values := url.Values{
		"apikey":   {"api-secret"},
		"nzbkey":   {"nzb-secret"},
		"token":    {"token-secret"},
		"username": {"admin"},
		"password": {"super-secret"},
		"pass":     {"archive-secret"},
		"mode":     {"addurl"},
	}

	got := sanitizeQuery(values)

	for _, key := range []string{"apikey", "nzbkey", "token", "username", "password", "pass"} {
		if got[key] != "[redacted]" {
			t.Fatalf("%s = %q, want [redacted]", key, got[key])
		}
	}
	if got["mode"] != "addurl" {
		t.Fatalf("mode = %q, want %q", got["mode"], "addurl")
	}
}
