package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/portbyte-id/server-panel/internal/jobs"
)

// handleJobsList answers GET /api/jobs: the jobs the page can watch, newest first.
//
// The events route needs a job id, so without this nothing could find a job the
// page did not just start. `state=active` returns queued and running jobs only,
// so a long job is not pushed out of the list by newer short ones.
func (s *server) handleJobsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	var opts jobs.ListOptions
	switch strings.TrimSpace(q.Get("state")) {
	case "", "all":
	case "active":
		opts.Active = true
	default:
		writeError(w, http.StatusBadRequest, errInvalidInput, `state must be "active" or "all"`)
		return
	}

	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > jobs.MaxListLimit {
			writeError(w, http.StatusBadRequest, errInvalidInput,
				"limit must be between 1 and "+strconv.Itoa(jobs.MaxListLimit))
			return
		}
		opts.Limit = n
	}

	list, err := s.deps.Jobs.List(r.Context(), opts)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": list})
}
