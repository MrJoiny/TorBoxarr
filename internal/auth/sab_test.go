package auth_test

import (
	"testing"

	"github.com/mrjoiny/torboxarr/internal/auth"
)

func TestSABAuth_AllowPublicModes(t *testing.T) {
	a := auth.NewSABAuth("apikey123", "nzbkey456")
	for _, mode := range []string{"version", "auth"} {
		if !a.Allow(mode, "") {
			t.Errorf("Allow(%q, \"\") = false, want true", mode)
		}
	}
}

func TestSABAuth_DenyWithoutKey(t *testing.T) {
	a := auth.NewSABAuth("apikey123", "nzbkey456")
	if a.Allow("addurl", "") {
		t.Error("Allow(addurl, \"\") = true, want false")
	}
}

func TestSABAuth_AllowWithAPIKey(t *testing.T) {
	a := auth.NewSABAuth("apikey123", "nzbkey456")
	for _, mode := range []string{"addurl", "addfile", "queue", "history", "get_config", "get_cats"} {
		if !a.Allow(mode, "apikey123") {
			t.Errorf("Allow(%q, apikey) = false, want true", mode)
		}
	}
}

func TestSABAuth_AllowWithNZBKey(t *testing.T) {
	a := auth.NewSABAuth("apikey123", "nzbkey456")
	if !a.Allow("addurl", "nzbkey456") {
		t.Error("Allow(addurl, nzbKey) = false, want true")
	}
}

func TestSABAuth_DenyWithWrongKey(t *testing.T) {
	a := auth.NewSABAuth("apikey123", "nzbkey456")
	if a.Allow("addurl", "wrongkey") {
		t.Error("Allow(addurl, wrongkey) = true, want false")
	}
}

func TestSABAuth_ModeCaseInsensitive(t *testing.T) {
	a := auth.NewSABAuth("apikey123", "nzbkey456")
	if !a.Allow("VERSION", "") {
		t.Error("Allow(VERSION, \"\") should be case-insensitive")
	}
}
