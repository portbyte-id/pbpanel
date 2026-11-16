package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// errorEnvelope is the only error shape the API emits. `error` is the stable,
// machine-readable class; `detail` is a sentence for the operator. A response
// never carries a stack trace, a driver error or a Go type name: those describe
// the server's internals to whoever asked.
type errorEnvelope struct {
	Error  string `json:"error"`
	Detail string `json:"detail"`
}

// Error classes. These are the values of the envelope's `error` field and the
// only thing a client should branch on.
const (
	errBadRequest     = "bad_request"
	errUnauthorized   = "unauthorized"
	errNotFound       = "not_found"
	errTooMany        = "too_many_requests"
	errInternal       = "internal_error"
	errInvalidCSRF    = "invalid_csrf_token"
	errInvalidLogin   = "invalid_credentials"
	errInvalidSetting = "invalid_setting"
)

// writeJSON sends v as JSON with status. Encoding happens into a buffer first
// so a marshal failure cannot leave a half-written body behind a status code
// that already promised a complete one.
func writeJSON(w http.ResponseWriter, status int, v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		// The value is built by this package, so this is a programming error.
		// Sending a fixed body keeps the response valid JSON either way.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal_error","detail":"response encoding failed"}` + "\n"))
		return
	}
	buf = append(buf, '\n')

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

// writeError sends the error envelope. detail must be written for the operator
// and must not quote an internal error verbatim; use writeInternal for that.
func writeError(w http.ResponseWriter, status int, class, detail string) {
	writeJSON(w, status, errorEnvelope{Error: class, Detail: detail})
}

// writeInternal logs err with the request ID and answers with a generic detail,
// so a database error reaches the log and not the client.
func (s *server) writeInternal(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.ErrorContext(r.Context(), "request failed",
		"error", err,
		"method", r.Method,
		"path", r.URL.Path,
		"request_id", requestIDFrom(r.Context()),
	)
	writeError(w, http.StatusInternalServerError, errInternal, "the request could not be completed")
}

// decodeJSON reads exactly one JSON value from the request body into v. It
// rejects a body over maxBody, trailing data after the value, and an unknown
// field, because a client sending a field this build does not know about has a
// different contract in mind than the one being served.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		return err
	}
	// A second Decode must report io.EOF: anything else is a second JSON value
	// in a body that the contract says holds one.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("body must contain exactly one JSON object")
	}
	return nil
}

// decodeError maps a decode failure to the detail shown to the client. The
// underlying message is safe to repeat for syntax and type errors (it names no
// internals), but the size limit's message is clearer rewritten.
func decodeErrorDetail(err error) string {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return "request body is too large"
	}
	if errors.Is(err, io.EOF) {
		return "request body is empty"
	}
	return "request body is not valid JSON for this endpoint: " + err.Error()
}
