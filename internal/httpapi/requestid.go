package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// requestIDHeader is where a request ID is read from and echoed back in. A
// client (or the smoke script) may supply its own so one operation can be
// followed across the browser, the panel log and the audit trail; a value is
// only accepted if it is short and made of URL-safe characters, so a header
// cannot inject a newline into a log line.
const requestIDHeader = "X-Request-ID"

const (
	requestIDBytes  = 12 // 96 bits: collision-free for a panel's request rate
	maxRequestIDLen = 64
)

type requestIDKey struct{}

// requestIDFrom returns the ID the request-id middleware attached, or "" when
// the request did not pass through it (a handler called directly in a test).
func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// withRequestID attaches an ID to the request context and echoes it back. It
// runs first in the chain so every later log line and the audit entry carry it.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func newRequestID() string {
	b := make([]byte, requestIDBytes)
	if _, err := rand.Read(b); err != nil {
		// A request must not fail because it could not be labelled. The empty
		// string is still a valid "no ID" for the log line.
		return ""
	}
	return hex.EncodeToString(b)
}
