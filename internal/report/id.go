// Package report implements the Permanent Link feature: an ephemeral,
// in-memory per-check result cache and a disk-backed persisted report
// store promoted from it. Both use the same ID scheme.
package report

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
)

// idBytes of crypto/rand entropy (160 bits) encoded as 40 lowercase hex
// characters. Comfortably above the ~122 bits of a random UUIDv4 — these
// IDs back a public, unauthenticated read endpoint under active pentest
// scrutiny and must not be brute-forceable or enumerable.
const idBytes = 20

var idRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// NewID returns a fresh random ID for either an ephemeral cache entry or a
// persisted report.
func NewID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ValidID reports whether s has the fixed length and charset produced by
// NewID. Callers must check this before using client-supplied input to
// build a filesystem path or as a cache lookup key.
func ValidID(s string) bool {
	return idRe.MatchString(s)
}
