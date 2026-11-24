package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/portbyte-id/server-panel/internal/update"
)

// UpdateService is the panel's release check and detached upgrade. The daemon
// builds one from *update.Service; tests substitute a fake.
type UpdateService interface {
	Info(ctx context.Context, force bool) update.Info
	Status() (update.Status, error)
	Start(ctx context.Context, tag string) (update.Status, error)
}

// updateRoutes registers the version check and the upgrade. They are registered
// even when Deps.Update is nil, so the UI gets a 503 that says why.
func (s *server) updateRoutes(mux *http.ServeMux) {
	h := func(fn func(UpdateService, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Update == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"updates are not available; see the daemon log")
				return
			}
			fn(s.deps.Update, w, r)
		}))
	}
	mux.Handle("GET /api/update", h(updateInfo))
	mux.Handle("GET /api/update/upgrade", h(updateStatus))
	mux.Handle("POST /api/update/upgrade", h(updateStart))
}

// updateInfo answers GET /api/update. ?refresh=1 asks GitHub again instead of
// reusing the cached answer. A failed check is still a 200: the body carries the
// error and the running version, which the footer needs either way.
func updateInfo(svc UpdateService, w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("refresh") == "1"
	writeJSON(w, http.StatusOK, svc.Info(r.Context(), force))
}

// updateStatus answers GET /api/update/upgrade with the latest upgrade's
// progress. The file it reads outlives the panel's restart, so the UI can
// follow an upgrade across it.
func updateStatus(svc UpdateService, w http.ResponseWriter, r *http.Request) {
	st, err := svc.Status()
	if err != nil {
		writeError(w, http.StatusInternalServerError, errInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type updateStartRequest struct {
	Tag string `json:"tag"`
}

// updateStart answers POST /api/update/upgrade. The upgrade runs as its own
// unit, so this returns as soon as the unit is queued, with 202.
func updateStart(svc UpdateService, w http.ResponseWriter, r *http.Request) {
	var req updateStartRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	tag := strings.TrimSpace(req.Tag)
	if tag == "" {
		tag = "latest"
	}

	st, err := svc.Start(r.Context(), tag)
	switch {
	case errors.Is(err, update.ErrRunning):
		writeError(w, http.StatusConflict, errConflict, "an upgrade is already running")
	case errors.Is(err, update.ErrInvalidTag):
		writeError(w, http.StatusBadRequest, errInvalidInput, "the release tag is not valid")
	case err != nil:
		writeError(w, http.StatusInternalServerError, errInternal, err.Error())
	default:
		writeJSON(w, http.StatusAccepted, st)
	}
}
