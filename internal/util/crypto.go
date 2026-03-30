package util

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
)

// RandomHex generates n random bytes and returns them as a hex-encoded string.
func RandomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// SHA1Hex returns the SHA-1 hash of v as a hex-encoded string.
func SHA1Hex(v string) string {
	sum := sha1.Sum([]byte(v))
	return hex.EncodeToString(sum[:])
}
