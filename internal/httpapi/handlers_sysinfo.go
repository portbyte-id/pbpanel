package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/sysinfo"
)

// SysinfoService is what the dashboard routes use: the host sampler and the
// service allowlist. The daemon builds one value out of a *sysinfo.Sampler and a
// *sysinfo.Services; tests substitute a fake.
type SysinfoService interface {
	Interval() time.Duration
	History() []sysinfo.Snapshot
	Latest() (sysinfo.Snapshot, bool)
	Subscribe() (<-chan sysinfo.Snapshot, func())
	List(ctx context.Context) []sysinfo.Service
	Control(ctx context.Context, name string, action sysinfo.Action) error
}

// Error classes of the dashboard routes.
const (
	errNotAllowed    = "not_allowed"
	errControlFailed = "control_failed"
)

// serviceControlTimeout bounds one start, stop or restart. The request's own
// context is not used: a browser that closes the tab halfway through a restart
// must not leave a container stopped.
const serviceControlTimeout = 60 * time.Second

// sysinfoRoutes registers the routes of development plan step 7.7. They are
// registered even when Deps.Sysinfo is nil, so the UI gets a 503 that says why.
func (s *server) sysinfoRoutes(mux *http.ServeMux) {
	h := func(fn func(SysinfoService, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Sysinfo == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"host metrics are not available; see the daemon log")
				return
			}
			fn(s.deps.Sysinfo, w, r)
		}))
	}
	mux.Handle("GET /api/sysinfo", h(s.sysinfoGet))
	mux.Handle("GET /api/sysinfo/stream", h(s.sysinfoStream))
	mux.Handle("GET /api/services", h(s.servicesList))
	mux.Handle("POST /api/services/{name}/{action}", h(s.servicesControl))
}

type sysinfoResponse struct {
	IntervalSeconds float64 `json:"interval_seconds"`
	// Latest is null before the first sample.
	Latest  *sysinfo.Snapshot  `json:"latest"`
	History []sysinfo.Snapshot `json:"history"`
}

func (s *server) sysinfoGet(svc SysinfoService, w http.ResponseWriter, r *http.Request) {
	hist := svc.History()
	if q := r.URL.Query().Get("limit"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, errBadRequest, "limit must be a non-negative integer")
			return
		}
		if n > 0 && n < len(hist) {
			hist = hist[len(hist)-n:]
		}
	}
	if hist == nil {
		hist = []sysinfo.Snapshot{}
	}
	out := sysinfoResponse{IntervalSeconds: svc.Interval().Seconds(), History: hist}
	if snap, ok := svc.Latest(); ok {
		out.Latest = &snap
	}
	writeJSON(w, http.StatusOK, out)
}

// sysinfoStream sends the newest snapshot at once and every later one as it is
// taken, as `snapshot` events, until the client goes away.
func (s *server) sysinfoStream(svc SysinfoService, w http.ResponseWriter, r *http.Request) {
	// Subscribe before reading Latest, so no snapshot falls between the two. A
	// snapshot sent twice is harmless: the client keys on its time.
	ch, cancel := svc.Subscribe()
	defer cancel()

	st := startSSE(w)
	if snap, ok := svc.Latest(); ok {
		if !st.event("snapshot", snap) {
			return
		}
	} else if !st.comment("waiting for the first sample") {
		return
	}

	beat := time.NewTicker(sseHeartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case snap, ok := <-ch:
			if !ok {
				return
			}
			if !st.event("snapshot", snap) {
				return
			}
		case <-beat.C:
			if !st.comment("keep-alive") {
				return
			}
		}
	}
}

func (s *server) servicesList(svc SysinfoService, w http.ResponseWriter, r *http.Request) {
	list := svc.List(r.Context())
	if list == nil {
		list = []sysinfo.Service{}
	}
	for i := range list {
		if list[i].Actions == nil {
			list[i].Actions = []sysinfo.Action{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": list})
}

func (s *server) servicesControl(svc SysinfoService, w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	action := sysinfo.Action(r.PathValue("action"))
	switch action {
	case sysinfo.ActionStart, sysinfo.ActionStop, sysinfo.ActionRestart:
	default:
		writeError(w, http.StatusBadRequest, errBadRequest, "the action must be start, stop or restart")
		return
	}
	if !fwEmptyBody(w, r) {
		return
	}
	noteTarget(r, "service:"+name)
	note(r, "action", string(action))

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), serviceControlTimeout)
	defer cancel()
	if err := svc.Control(ctx, name, action); err != nil {
		s.servicesFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxControlOutput caps the command text echoed back on a failed control, which
// is systemctl's or Docker's own message about the operator's own service.
const maxControlOutput = 1000

func (s *server) servicesFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, sysinfo.ErrUnknownService):
		writeError(w, http.StatusNotFound, errNotFound, "no such service")
	case errors.Is(err, sysinfo.ErrUnknownAction):
		writeError(w, http.StatusBadRequest, errBadRequest, "the action must be start, stop or restart")
	case errors.Is(err, sysinfo.ErrNotAllowed):
		writeError(w, http.StatusForbidden, errNotAllowed, "that action is not allowed on this service")
	default:
		s.logger.WarnContext(r.Context(), "service control failed",
			"error", err, "path", r.URL.Path, "request_id", requestIDFrom(r.Context()))
		out := strings.TrimSpace(err.Error())
		if len(out) > maxControlOutput {
			out = out[:maxControlOutput]
		}
		writeJSON(w, http.StatusBadGateway, errorEnvelope{
			Error: errControlFailed, Detail: "the service could not be controlled", Output: out})
	}
}
