package torbox

import "testing"

func TestSanitizeLoggedPath_RedactsToken(t *testing.T) {
	got := sanitizeLoggedPath("/api/torrents/requestdl?token=secret-token&redirect=false")
	want := "/api/torrents/requestdl?redirect=false&token=%5Bredacted%5D"
	if got != want {
		t.Fatalf("sanitizeLoggedPath() = %q, want %q", got, want)
	}
}

func TestSanitizeLoggedPath_LeavesSafeQueriesUntouched(t *testing.T) {
	got := sanitizeLoggedPath("/api/torrents/mylist?id=123&bypass_cache=true")
	want := "/api/torrents/mylist?bypass_cache=true&id=123"
	if got != want {
		t.Fatalf("sanitizeLoggedPath() = %q, want %q", got, want)
	}
}
