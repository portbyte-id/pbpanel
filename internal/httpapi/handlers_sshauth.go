package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/sshauth"
)

// SSHAuthMonitor is the part of *sshauth.Monitor the routes use; tests
// substitute a fake.
type SSHAuthMonitor interface {
	Attempts(ctx context.Context, q sshauth.Query) (sshauth.Report, error)
	Available(ctx context.Context) error
}

// sshMaxLimit bounds the limit parameter. A request above it is refused rather
// than cut: the page asks for what it shows, and a silent cut would make the
// total and the page disagree without saying so.
const sshMaxLimit = sshauth.MaxLimit

// sshOutcomes are the values the outcome parameter accepts.
var sshOutcomes = map[string]sshauth.Outcome{
	"":        "",
	"failed":  sshauth.OutcomeFailed,
	"success": sshauth.OutcomeSuccess,
}

// sshAuthRoutes registers the SSH login-attempt monitor. They stay registered
// when Deps.SSHAuth is nil, so the UI gets a 503 that says why.
//
// One route answers both halves of the page: the feed carries its own summary,
// and a separate /summary route would make a second journal pass over the same
// window for a value the first already computed.
func (s *server) sshAuthRoutes(mux *http.ServeMux) {
	h := func(fn func(SSHAuthMonitor, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.SSHAuth == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"the SSH monitor is not available; see the daemon log")
				return
			}
			fn(s.deps.SSHAuth, w, r)
		}))
	}
	mux.Handle("GET /api/ssh/attempts", h(s.sshAttempts))
}

func (s *server) sshAttempts(m SSHAuthMonitor, w http.ResponseWriter, r *http.Request) {
	q, ok := parseSSHQuery(w, r)
	if !ok {
		return
	}
	rep, err := m.Attempts(r.Context(), q)
	if err != nil {
		if errors.Is(err, sshauth.ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, errUnavailable,
				"the sshd journal cannot be read on this host")
			return
		}
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// parseSSHQuery reads the parameters, answers 400 for the first bad one, and
// reports false. Every value is checked here, so the Monitor is never asked for
// a window or a limit it would have to clamp silently.
func parseSSHQuery(w http.ResponseWriter, r *http.Request) (sshauth.Query, bool) {
	var q sshauth.Query
	params := r.URL.Query()
	bad := func(field, detail string) (sshauth.Query, bool) {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errBadRequest, Detail: detail, Field: field})
		return sshauth.Query{}, false
	}

	if raw := strings.TrimSpace(params.Get("range")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return bad("range", "range must be a duration such as 24h or 90m")
		}
		if d < sshauth.MinRange || d > sshauth.MaxRange {
			return bad("range", "range must be between 5m and 720h")
		}
		q.Range = d
	}

	if raw := strings.TrimSpace(params.Get("outcome")); raw != "" {
		o, ok := sshOutcomes[strings.ToLower(raw)]
		if !ok {
			return bad("outcome", "outcome must be failed or success")
		}
		q.Outcome = o
	}

	if raw := strings.TrimSpace(params.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return bad("limit", "limit must be a positive number")
		}
		if n > sshMaxLimit {
			return bad("limit", "limit must be at most "+strconv.Itoa(sshMaxLimit))
		}
		q.Limit = n
	}
	return q, true
}
