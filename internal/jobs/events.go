package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// EventsPattern is the route the manager serves. httpapi mounts it; this package
// only provides the handler.
const EventsPattern = "GET /api/jobs/{id}/events"

// ServeEvents streams a job's output as Server-Sent Events.
//
// The client's position comes from `Last-Event-ID` (set automatically by
// EventSource on reconnect) or, for clients that cannot set headers, from a
// `last_event_id` query parameter. Everything after that sequence number is
// replayed from the database, then live lines follow; each line carries its seq
// as the SSE id, so the next reconnect resumes exactly where this one stopped.
//
// The subscription is opened before the replay, not after, so a line written
// between the two is not lost. That ordering can deliver a line twice, which the
// seq check in the live loop removes.
func (m *Manager) ServeEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "missing job id")
		return
	}

	after, err := lastEventID(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	job, err := m.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	live, unsubscribe, err := m.Subscribe(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer unsubscribe()

	replayed, err := m.Replay(r.Context(), id, after)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Headers go out only once the replay has been read: a failure above is still
	// an ordinary JSON error the client can act on.
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// nginx buffers proxied responses by default, which would hold a stream until
	// the job ended; the panel sits behind one when it is served from a site.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	emit := func(ln Line) bool {
		if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ln.Seq, ln.Text); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	last := after
	for _, ln := range replayed {
		if !emit(ln) {
			return
		}
		last = ln.Seq
	}

	// A truncated replay starts mid-stream; say so before the live lines so the
	// UI can show "earlier output dropped" instead of a log that appears to begin
	// with the middle of a line.
	if job.Truncated && after < job.Dropped {
		_, _ = fmt.Fprintf(w, "event: truncated\ndata: %s\n\n", mustJSON(map[string]any{"dropped": job.Dropped, "last_seq": job.LastSeq}))
		if rc.Flush() != nil {
			return
		}
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case ln, ok := <-live:
			if !ok {
				// A closed channel means either the job ended or this subscriber
				// fell behind and was dropped. Only the first is an `end`; the
				// second tells the client to reconnect with Last-Event-ID, or it
				// would stop reading a job that is still producing output.
				if state := m.finalState(r, id); state.State.Terminal() {
					_, _ = fmt.Fprintf(w, "event: end\ndata: %s\n\n", mustJSON(state))
				} else {
					_, _ = fmt.Fprintf(w, "event: dropped\ndata: %s\n\n", mustJSON(map[string]any{"last_seq": last}))
				}
				_ = rc.Flush()
				return
			}
			if ln.Seq <= last {
				continue // already sent during replay
			}
			if !emit(ln) {
				return
			}
			last = ln.Seq
		}
	}
}

// finalState re-reads the job so the closing event carries the stored terminal
// row rather than whatever the in-memory copy said.
func (m *Manager) finalState(r *http.Request, id string) Job {
	job, err := m.Get(r.Context(), id)
	if err != nil {
		return Job{ID: id}
	}
	return job
}

func lastEventID(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("last_event_id"))
	}
	if raw == "" {
		return 0, nil
	}
	seq, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seq < 0 {
		return 0, fmt.Errorf("Last-Event-ID %q is not a sequence number", raw)
	}
	return seq, nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	return b
}

// writeJSONError matches the httpapi envelope so a client parses one shape from
// every route.
func writeJSONError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": http.StatusText(status), "detail": detail})
}
