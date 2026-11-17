package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/portbyte-id/server-panel/internal/logs"
)

// LogsService is the part of *logs.Registry the routes use; tests substitute a
// fake.
type LogsService interface {
	List(ctx context.Context) ([]logs.Source, error)
	Resolve(ctx context.Context, id string) (logs.Source, error)
	TailChan(ctx context.Context, id string, opts logs.TailOptions) (<-chan string, <-chan error)
	Search(ctx context.Context, id string, q logs.Query, opts logs.SearchOptions, emit func(logs.Match) error) (logs.SearchStats, error)
}

const (
	errSourceUnavailable = "source_unavailable"

	// tailCommitAfter is how long a tail waits for its first line or its first
	// error before it commits to a 200 stream. An error that arrives sooner (a
	// log that does not exist yet) is answered with a real status instead.
	tailCommitAfter = 300 * time.Millisecond

	// searchFlushEvery is how many matches are written between flushes.
	searchFlushEvery = 64
)

// logsRoutes registers the log viewer routes of development plan step 7.7.
func (s *server) logsRoutes(mux *http.ServeMux) {
	h := func(fn func(LogsService, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Logs == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"the log viewer is not available; see the daemon log")
				return
			}
			fn(s.deps.Logs, w, r)
		}))
	}
	mux.Handle("GET /api/logs", h(s.logsList))
	mux.Handle("GET /api/logs/{id}/tail", h(s.logsTail))
	mux.Handle("GET /api/logs/{id}/search", h(s.logsSearch))
}

func (s *server) logsFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, logs.ErrNotFound):
		writeError(w, http.StatusNotFound, errNotFound, "no such log")
	case errors.Is(err, logs.ErrBadQuery):
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errInvalidInput, Detail: err.Error(), Field: "q"})
	case errors.Is(err, logs.ErrUnavailable):
		writeError(w, http.StatusConflict, errSourceUnavailable,
			"that log cannot be read right now; it may not exist yet, or Docker is not reachable")
	default:
		s.writeInternal(w, r, err)
	}
}

func (s *server) logsList(l LogsService, w http.ResponseWriter, r *http.Request) {
	list, err := l.List(r.Context())
	if err != nil {
		// A provider that fails does not hide the others; the viewer shows what
		// could be listed, and the cause is in the daemon log.
		s.logger.WarnContext(r.Context(), "a log source provider failed",
			"error", err, "request_id", requestIDFrom(r.Context()))
	}
	if list == nil {
		list = []logs.Source{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": list})
}

// queryInt reads an optional non-negative integer query parameter.
func queryInt(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errBadRequest,
			Detail: name + " must be a non-negative integer", Field: name})
		return 0, false
	}
	return n, true
}

// queryBool reads an optional boolean query parameter (1, true, 0, false).
func queryBool(w http.ResponseWriter, r *http.Request, name string) (bool, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return false, true
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errBadRequest,
			Detail: name + " must be true or false", Field: name})
		return false, false
	}
	return b, true
}

// logsTail streams a log as server-sent events: the last `lines` lines, then with
// follow=1 every new line. Events: `line` {"line": "..."}, `error` {error,
// detail} (a stream that failed after it started), and `end` {} when a
// non-followed tail finished. A log that cannot be opened is a plain JSON error
// before the stream starts.
func (s *server) logsTail(l LogsService, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	lines, ok := queryInt(w, r, "lines")
	if !ok {
		return
	}
	follow, ok := queryBool(w, r, "follow")
	if !ok {
		return
	}
	if _, err := l.Resolve(r.Context(), id); err != nil {
		s.logsFail(w, r, err)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	ch, errc := l.TailChan(ctx, id, logs.TailOptions{Lines: lines, Follow: follow})

	var (
		st      *sseStream
		tailErr error
	)
	commit := func() { // idempotent
		if st == nil {
			st = startSSE(w)
		}
	}
	grace := time.NewTimer(tailCommitAfter)
	defer grace.Stop()
	beat := time.NewTicker(sseHeartbeat)
	defer beat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-grace.C:
			commit()
		case <-beat.C:
			if st != nil && !st.comment("keep-alive") {
				return
			}
		case err := <-errc:
			// The reason arrives before the lines channel closes; the lines that
			// precede it are still to be sent.
			tailErr = err
			errc = nil
		case line, open := <-ch:
			if open {
				commit()
				if !st.event("line", map[string]string{"line": line}) {
					return
				}
				continue
			}
			// The reason is sent before the lines channel closes, so it is
			// there now even if the select above never picked it.
			if errc != nil {
				select {
				case tailErr = <-errc:
				default:
				}
			}
			s.finishTail(w, r, st, tailErr)
			return
		}
	}
}

func (s *server) finishTail(w http.ResponseWriter, r *http.Request, st *sseStream, err error) {
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return
	}
	if st == nil {
		if err != nil {
			s.logsFail(w, r, err)
			return
		}
		st = startSSE(w)
	}
	if err != nil {
		class, detail := errInternal, "the log stream failed"
		if errors.Is(err, logs.ErrUnavailable) {
			class, detail = errSourceUnavailable, "that log cannot be read right now"
		}
		s.logger.WarnContext(r.Context(), "log tail failed",
			"error", err, "path", r.URL.Path, "request_id", requestIDFrom(r.Context()))
		st.event("error", errorEnvelope{Error: class, Detail: detail})
		return
	}
	st.event("end", struct{}{})
}

// searchLine is one line of the NDJSON a search answers with.
type searchLine struct {
	Type string `json:"type"`
	// match
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Partial bool   `json:"partial,omitempty"`
	Text    string `json:"text,omitempty"`
	// done
	Stats *logs.SearchStats `json:"stats,omitempty"`
	// error
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// logsSearch answers with newline-delimited JSON (application/x-ndjson): one
// {"type":"match",file,line,partial,text} per hit, oldest first, then exactly one
// {"type":"done","stats":{...}} - or {"type":"error",error,detail} if the search
// failed after it had started. A request that fails before the first hit (an
// unknown log, a bad pattern) is the usual JSON error with a status instead.
// There is no heartbeat: the response is finite and bounded by last_mb.
func (s *server) logsSearch(l LogsService, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()
	pattern := q.Get("q")
	if pattern == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errInvalidInput, Detail: "q is required", Field: "q"})
		return
	}
	regex, ok := queryBool(w, r, "regex")
	if !ok {
		return
	}
	ci, ok := queryBool(w, r, "ci")
	if !ok {
		return
	}
	lastMB, ok := queryInt(w, r, "last_mb")
	if !ok {
		return
	}
	limit, ok := queryInt(w, r, "limit")
	if !ok {
		return
	}

	var (
		started bool
		enc     *json.Encoder
		rc      = http.NewResponseController(w)
		pending int
	)
	start := func() {
		if started {
			return
		}
		started = true
		h := w.Header()
		h.Set("Content-Type", "application/x-ndjson")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		_ = rc.SetWriteDeadline(time.Time{})
		enc = json.NewEncoder(w)
	}

	stats, err := l.Search(r.Context(), id,
		logs.Query{Pattern: pattern, Regex: regex, CaseInsensitive: ci},
		logs.SearchOptions{LastMB: lastMB, Limit: limit},
		func(m logs.Match) error {
			start()
			if err := enc.Encode(searchLine{Type: "match", File: m.File, Line: m.Line, Partial: m.Partial, Text: m.Text}); err != nil {
				return err
			}
			if pending++; pending%searchFlushEvery == 0 {
				return rc.Flush()
			}
			return nil
		})

	if err != nil && r.Context().Err() != nil {
		return // the client left; there is nobody to tell
	}
	if err != nil && !started {
		s.logsFail(w, r, err)
		return
	}
	start()
	if err != nil {
		s.logger.WarnContext(r.Context(), "log search failed",
			"error", err, "path", r.URL.Path, "request_id", requestIDFrom(r.Context()))
		_ = enc.Encode(searchLine{Type: "error", Error: errInternal, Detail: "the search failed part way"})
	} else {
		_ = enc.Encode(searchLine{Type: "done", Stats: &stats})
	}
	_ = rc.Flush()
}
