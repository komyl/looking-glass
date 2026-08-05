package report

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
)

// Comfortably above the ~122 bits of a random UUIDv4 — these IDs back a
// public, unauthenticated read endpoint under active pentest scrutiny and
// must not be brute-forceable or enumerable.
const idBytes = 20

var idRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

func NewID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Callers must check this before using client-supplied input to build a
// filesystem path or as a cache lookup key.
func ValidID(s string) bool {
	return idRe.MatchString(s)
}
