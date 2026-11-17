package httpapi

import (
	"context"
	"net/http"
	"sync"

	"github.com/portbyte-id/server-panel/internal/audit"
)

// statusRecorder captures the status code a handler chose so the audit
// middleware can record the outcome. It passes every other method straight
// through, so wrapping does not cost a handler anything it needs (flushing,
// hijacking, streaming).
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer, which is
// what the SSE handler in internal/jobs uses to flush.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) code() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// withAudit records every request that could have changed state: every unsafe
// method, successful or not. Reads are not audited, because the trail exists to
// answer "who changed this", and logging every poll would bury that answer.
//
// A failed audit write is logged and does not change the response: the action
// already happened, so failing the request afterwards would be a lie about what
// the panel did.
func (s *server) withAudit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		rec := &statusRecorder{ResponseWriter: w}
		notes := &auditNotes{}
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), auditNotesKey{}, notes)))

		result := audit.Failure
		if rec.code() < http.StatusBadRequest {
			result = audit.Success
		}

		detail := map[string]any{
			"status":     rec.code(),
			"request_id": requestIDFrom(r.Context()),
			"ip":         clientIP(r),
		}
		for k, v := range notes.snapshot() {
			detail[k] = v
		}
		entry := audit.Entry{
			Actor:  s.actorFor(r),
			Action: r.Method + " " + r.URL.Path,
			Target: r.URL.Path,
			Result: result,
			Detail: detail,
		}
		if t := notes.target(); t != "" {
			entry.Target = t
		}
		if err := s.deps.Audit.Write(r.Context(), entry); err != nil {
			s.logger.ErrorContext(r.Context(), "audit write failed",
				"error", err, "action", entry.Action, "request_id", requestIDFrom(r.Context()))
		}
	})
}

// actorFor names who performed the request. The panel has one admin account, so
// every request is attributed to it; the body's username is never recorded,
// because a failed login's username is attacker-supplied text.
func (s *server) actorFor(*http.Request) string {
	return s.deps.Actor
}

type auditNotesKey struct{}

// auditNotes lets a handler add to the audit entry the middleware writes after
// it returns: the path a file operation touched is in the body or the query,
// where the middleware cannot see it without parsing every route's input.
type auditNotes struct {
	mu   sync.Mutex
	m    map[string]any
	targ string
}

func (n *auditNotes) snapshot() map[string]any {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make(map[string]any, len(n.m))
	for k, v := range n.m {
		out[k] = v
	}
	return out
}

func (n *auditNotes) target() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.targ
}

// note records key=value in the request's audit entry. It is a no-op for a
// request that did not pass through the audit middleware (a safe method).
func note(r *http.Request, key string, value any) {
	if n, ok := r.Context().Value(auditNotesKey{}).(*auditNotes); ok {
		n.mu.Lock()
		if n.m == nil {
			n.m = map[string]any{}
		}
		n.m[key] = value
		n.mu.Unlock()
	}
}

// noteTarget replaces the entry's target (by default the URL path) with what
// the request acted on.
func noteTarget(r *http.Request, target string) {
	if n, ok := r.Context().Value(auditNotesKey{}).(*auditNotes); ok {
		n.mu.Lock()
		n.targ = target
		n.mu.Unlock()
	}
}
