package httpapi

import (
	"net/http"
	"runtime/debug"
)

// withRecover turns a panic in any later middleware or handler into a logged
// 500 with the JSON error envelope, so one bad request cannot take the daemon
// down and a client always gets a body it can parse.
//
// The stack goes to the log and never into the response.
func (s *server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// ErrAbortHandler is the documented way for a handler to abort
			// silently; the connection is already being torn down and writing a
			// body to it would only log a second, misleading error.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}

			s.logger.ErrorContext(r.Context(), "panic serving request",
				"panic", rec,
				"method", r.Method,
				"path", r.URL.Path,
				"request_id", requestIDFrom(r.Context()),
				"stack", string(debug.Stack()),
			)
			// Headers may already be committed (a streamed response, say); in
			// that case the status is set and this write is a no-op, which is
			// the best that can be done once bytes are on the wire.
			writeError(w, http.StatusInternalServerError, errInternal, "the request could not be completed")
		}()
		next.ServeHTTP(w, r)
	})
}
