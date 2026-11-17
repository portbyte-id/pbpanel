package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// sseHeartbeat is how often an idle event stream is touched, so a proxy or the
// browser does not drop it as dead. It is a comment line, which EventSource
// ignores.
const sseHeartbeat = 15 * time.Second

// sseStream writes server-sent events and flushes after each one. It goes
// through http.ResponseController, so it reaches the real writer behind the
// audit middleware's recorder.
type sseStream struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// startSSE commits a 200 text/event-stream response. After this, errors can only
// be sent as events.
func startSSE(w http.ResponseWriter) *sseStream {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &sseStream{w: w, rc: http.NewResponseController(w)}
	// An open stream outlives the server's write deadline by design; clear it
	// where the writer allows. A writer that cannot is left as it is.
	_ = s.rc.SetWriteDeadline(time.Time{})
	return s
}

// event sends one named event with a JSON body. It reports false when the client
// is gone and the handler should return.
func (s *sseStream) event(name string, v any) bool {
	buf, err := json.Marshal(v)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, buf); err != nil {
		return false
	}
	return s.rc.Flush() == nil
}

// comment sends a comment line, the heartbeat.
func (s *sseStream) comment(text string) bool {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return false
	}
	return s.rc.Flush() == nil
}
