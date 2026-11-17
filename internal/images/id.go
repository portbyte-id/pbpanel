package images

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

// idRe is the shape NewID produces. A build ID ends up in an image reference
// and in a directory name, so nothing else is accepted.
var idRe = regexp.MustCompile(`^[0-9]{14}-[0-9a-f]{6}$`)

// NewID returns a build ID: the UTC time to the second, then six random hex
// digits, so two builds started in the same second still differ.
func NewID(now time.Time) (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("images: build id: %w", err)
	}
	return now.UTC().Format("20060102150405") + "-" + hex.EncodeToString(b[:]), nil
}

// CheckID accepts only what NewID returns.
func CheckID(id string) error {
	if !idRe.MatchString(id) {
		return fmt.Errorf("images: %q is not a build id", id)
	}
	return nil
}

// Tag is the reference a build is stored under: `panel/php:8.3-<id>`.
func Tag(repo, version, id string) string {
	return repo + ":" + version + "-" + id
}
