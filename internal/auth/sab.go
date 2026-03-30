package auth

import (
	"crypto/subtle"
	"strings"
)

type SABAuth struct {
	apiKey string
	nzbKey string
}

func NewSABAuth(apiKey, nzbKey string) *SABAuth {
	return &SABAuth{apiKey: apiKey, nzbKey: nzbKey}
}

func (a *SABAuth) Allow(mode, key string) bool {
	mode = strings.ToLower(strings.TrimSpace(mode))
	key = strings.TrimSpace(key)
	validKey := key != "" && (subtle.ConstantTimeCompare([]byte(key), []byte(a.apiKey)) == 1 ||
		subtle.ConstantTimeCompare([]byte(key), []byte(a.nzbKey)) == 1)
	switch mode {
	case "version", "auth":
		return true
	case "get_config", "get_cats", "set_config":
		return validKey
	case "addurl", "addfile":
		return validKey
	case "queue":
		return validKey
	case "history":
		return validKey
	default:
		return validKey
	}
}
