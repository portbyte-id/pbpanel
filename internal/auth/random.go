package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// randomToken returns n random bytes, base64url-encoded without padding. 32
// bytes gives the 256 bits decision 5 asks for in a session ID and is more than
// enough for a CSRF token.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
