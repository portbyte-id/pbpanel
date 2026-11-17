package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/apps"
	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/runtimes"
)

// AppsService is the part of apps.Manager the routes use. *apps.Manager
// implements it; tests substitute a fake.
type AppsService interface {
	List(ctx context.Context) ([]apps.Status, error)
	Get(ctx context.Context, name string) (apps.Status, error)
	Create(ctx context.Context, in apps.Input) (apps.Status, []string, error)
	Update(ctx context.Context, name string, in apps.Input) (apps.Status, []string, error)
	Delete(ctx context.Context, name string) error
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Restart(ctx context.Context, name string) error
	Log(ctx context.Context, name string, offset int64, max int) (apps.LogChunk, error)
}

// RuntimesService is the part of runtimes.Manager the routes use.
type RuntimesService interface {
	List(ctx context.Context) (runtimes.State, error)
	Install(ctx context.Context, version string, onLine func(string)) error
	Uninstall(ctx context.Context, full string, onLine func(string)) error
}

const (
	logPollEvery    = 700 * time.Millisecond
	logHeartbeat    = 15 * time.Second
	logInitialBytes = 64 << 10
)

// appsRoutes registers the routes of development plan step 5.7. They are
// registered even when the services are nil, so the UI gets a 503 that says why.
func (s *server) appsRoutes(mux *http.ServeMux) {
	ha := func(fn func(AppsService, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Apps == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"apps are not available on this host; see the daemon log")
				return
			}
			fn(s.deps.Apps, w, r)
		}))
	}
	hr := func(fn func(RuntimesService, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Runtimes == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"Node.js runtimes are not available on this host; see the daemon log")
				return
			}
			fn(s.deps.Runtimes, w, r)
		}))
	}

	mux.Handle("GET /api/apps", ha(s.appsList))
	mux.Handle("POST /api/apps", ha(s.appsCreate))
	mux.Handle("GET /api/apps/{name}", ha(s.appsGet))
	mux.Handle("PUT /api/apps/{name}", ha(s.appsUpdate))
	mux.Handle("DELETE /api/apps/{name}", ha(s.appsDelete))
	mux.Handle("POST /api/apps/{name}/start", ha(s.appsControl("start")))
	mux.Handle("POST /api/apps/{name}/stop", ha(s.appsControl("stop")))
	mux.Handle("POST /api/apps/{name}/restart", ha(s.appsControl("restart")))
	mux.Handle("GET /api/apps/{name}/logs", ha(s.appsLog))
	mux.Handle("GET /api/apps/{name}/logs/stream", ha(s.appsLogStream))

	mux.Handle("GET /api/runtimes", hr(s.runtimesList))
	mux.Handle("POST /api/runtimes/node", hr(s.runtimesInstall))
	mux.Handle("DELETE /api/runtimes/node/{version}", hr(s.runtimesUninstall))
}

// appsFail maps an error from the apps or confedit packages to the envelope.
// Anything unrecognised is an internal error: its text may carry paths or RPC
// output the client has no use for.
func (s *server) appsFail(w http.ResponseWriter, r *http.Request, err error) {
	var (
		invalid *apps.ValidationError
		apply   *confedit.ApplyError
	)
	switch {
	// Checked before the apply error it is usually wrapped in: supervisord being
	// down is the reason, and the files were put back.
	case errors.Is(err, apps.ErrNoSupervisor):
		writeError(w, http.StatusServiceUnavailable, errUnavailable,
			"supervisord is not running, so nothing was changed; check panel-supervisord.service")
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope{Error: errInvalidInput, Detail: invalid.Error(), Field: invalid.Field})
	case errors.Is(err, apps.ErrNotFound):
		writeError(w, http.StatusNotFound, errNotFound, "no such app")
	case errors.Is(err, apps.ErrExists):
		writeError(w, http.StatusConflict, errConflict, "an app with that name already exists")
	case errors.Is(err, apps.ErrPortInUse):
		writeJSON(w, http.StatusConflict, errorEnvelope{Error: errConflict, Detail: "another app already uses that port", Field: "port"})
	case errors.Is(err, apps.ErrNodeMissing):
		writeJSON(w, http.StatusConflict, errorEnvelope{Error: errConflict,
			Detail: "that Node.js version is not installed; install it on the Runtimes page first", Field: "node_version"})
	case errors.Is(err, apps.ErrAlreadyRunning):
		writeError(w, http.StatusConflict, errConflict, "the app is already running")
	case errors.Is(err, apps.ErrNoProcess):
		writeError(w, http.StatusConflict, errConflict, "supervisord has not loaded that app; save it again to load it")
	case errors.As(err, &apply):
		s.appsApplyError(w, r, apply)
	default:
		s.writeInternal(w, r, err)
	}
}

// appsApplyError is writeApplyError with the right name in the sentence: the
// service that rejected the file is supervisord, not nginx.
func (s *server) appsApplyError(w http.ResponseWriter, r *http.Request, ae *confedit.ApplyError) {
	s.logger.WarnContext(r.Context(), "app change not applied",
		"stage", ae.Stage, "service", ae.Service, "error", ae.Err, "rollback_error", ae.RollbackErr,
		"request_id", requestIDFrom(r.Context()))

	env := errorEnvelope{Output: ae.Output}
	status := http.StatusBadGateway
	switch ae.Stage {
	case confedit.StageValidate:
		status = http.StatusUnprocessableEntity
		env.Error = errConfigRejected
		env.Detail = "supervisord rejected the program file; the previous files are back in place"
	case confedit.StagePrepare:
		status = http.StatusBadRequest
		env.Error = errBadRequest
		env.Detail = "the change could not be prepared: " + ae.Err.Error()
	default:
		env.Error = errApplyFailed
		env.Detail = "the change failed at the " + string(ae.Stage) + " stage and was rolled back"
	}
	if ae.RollbackErr != nil {
		status = http.StatusInternalServerError
		env.Error = errApplyFailed
		env.Detail = "the change failed at the " + string(ae.Stage) +
			" stage and restoring the previous files failed too; they will be restored at the next daemon start"
	}
	writeJSON(w, status, env)
}

type appResponse struct {
	App      apps.Status `json:"app"`
	Warnings []string    `json:"warnings"`
}

func warnings(w []string) []string {
	if w == nil {
		return []string{}
	}
	return w
}

func (s *server) appsList(a AppsService, w http.ResponseWriter, r *http.Request) {
	list, err := a.List(r.Context())
	if err != nil {
		s.appsFail(w, r, err)
		return
	}
	if list == nil {
		list = []apps.Status{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": list})
}

func (s *server) appsGet(a AppsService, w http.ResponseWriter, r *http.Request) {
	st, err := a.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		s.appsFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, appResponse{App: st, Warnings: []string{}})
}

func (s *server) appsCreate(a AppsService, w http.ResponseWriter, r *http.Request) {
	var in apps.Input
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	st, warn, err := a.Create(r.Context(), in)
	if err != nil {
		s.appsFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, appResponse{App: st, Warnings: warnings(warn)})
}

func (s *server) appsUpdate(a AppsService, w http.ResponseWriter, r *http.Request) {
	var in apps.Input
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	st, warn, err := a.Update(r.Context(), r.PathValue("name"), in)
	if err != nil {
		s.appsFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, appResponse{App: st, Warnings: warnings(warn)})
}

func (s *server) appsDelete(a AppsService, w http.ResponseWriter, r *http.Request) {
	if err := a.Delete(r.Context(), r.PathValue("name")); err != nil {
		s.appsFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// appsControl builds the handler of start, stop and restart. The response is the
// app as supervisord now reports it.
func (s *server) appsControl(action string) func(AppsService, http.ResponseWriter, *http.Request) {
	return func(a AppsService, w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		var err error
		switch action {
		case "start":
			err = a.Start(r.Context(), name)
		case "stop":
			err = a.Stop(r.Context(), name)
		default:
			err = a.Restart(r.Context(), name)
		}
		if err != nil {
			s.appsFail(w, r, err)
			return
		}
		st, err := a.Get(r.Context(), name)
		if err != nil {
			s.appsFail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, appResponse{App: st, Warnings: []string{}})
	}
}

// logParams reads ?offset= and ?max=. A missing offset means "the tail".
func logParams(r *http.Request) (offset int64, max int, err error) {
	offset = -1
	if v := r.URL.Query().Get("offset"); v != "" {
		if offset, err = strconv.ParseInt(v, 10, 64); err != nil {
			return 0, 0, fmt.Errorf("offset must be a number")
		}
	}
	if v := r.URL.Query().Get("max"); v != "" {
		if max, err = strconv.Atoi(v); err != nil || max < 0 {
			return 0, 0, fmt.Errorf("max must be a positive number")
		}
	}
	return offset, max, nil
}

func (s *server) appsLog(a AppsService, w http.ResponseWriter, r *http.Request) {
	offset, max, err := logParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, err.Error())
		return
	}
	chunk, err := a.Log(r.Context(), r.PathValue("name"), offset, max)
	if err != nil {
		s.appsFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, chunk)
}

// appsLogStream follows an app's log over server-sent events: one `log` event per
// chunk, carrying the same JSON as GET .../logs, so a client keeps the offset of
// the last one and can resume from it after a dropped connection.
func (s *server) appsLogStream(a AppsService, w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	offset, _, err := logParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, err.Error())
		return
	}
	// The first read is also the existence check, so a missing app is a 404 and
	// not an empty stream.
	first := offset
	max := 0
	if first < 0 {
		max = logInitialBytes
	}
	chunk, err := a.Log(r.Context(), name, first, max)
	if err != nil {
		s.appsFail(w, r, err)
		return
	}

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(c apps.LogChunk) bool {
		buf, _ := json.Marshal(c)
		if _, err := fmt.Fprintf(w, "event: log\ndata: %s\n\n", buf); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send(chunk) {
		return
	}
	offset = chunk.Offset

	poll, beat := time.NewTicker(logPollEvery), time.NewTicker(logHeartbeat)
	defer poll.Stop()
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-poll.C:
			c, err := a.Log(r.Context(), name, offset, 0)
			if err != nil {
				return
			}
			// A shorter file than the offset means it rotated: Log read the new
			// tail, and the client is told so it can start its view over.
			if c.Data == "" && c.Offset == offset {
				continue
			}
			if !send(c) {
				return
			}
			offset = c.Offset
		}
	}
}

// --- runtimes ----------------------------------------------------------------

func (s *server) runtimesList(rt RuntimesService, w http.ResponseWriter, r *http.Request) {
	st, err := rt.List(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *server) runtimesInstall(rt RuntimesService, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	if !runtimes.ValidRequest(req.Version) {
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope{Error: errInvalidInput,
			Detail: "use a version such as 22, 22.11 or 22.11.0", Field: "version"})
		return
	}
	// The job outlives the request that started it.
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), "runtimes.node.install",
		func(ctx context.Context, out *jobs.Writer) error {
			return rt.Install(ctx, req.Version, func(line string) { _ = out.Line(line) })
		})
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Job jobs.Job `json:"job"`
	}{job})
}

func (s *server) runtimesUninstall(rt RuntimesService, w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	if !runtimes.ValidFull(version) {
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope{Error: errInvalidInput,
			Detail: "use the full installed version, such as 22.11.0", Field: "version"})
		return
	}
	// Checked here, before a job exists, so the operator gets the names of the
	// apps in the answer and not in a job log.
	st, err := rt.List(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	found := false
	for _, n := range st.Node {
		if n.Version != version {
			continue
		}
		found = true
		if len(n.UsedBy) > 0 {
			writeError(w, http.StatusConflict, errConflict,
				"apps still run on Node.js "+version+": "+strings.Join(n.UsedBy, ", "))
			return
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, errNotFound, "that Node.js version is not installed")
		return
	}
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), "runtimes.node.uninstall",
		func(ctx context.Context, out *jobs.Writer) error {
			return rt.Uninstall(ctx, version, func(line string) { _ = out.Line(line) })
		})
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Job jobs.Job `json:"job"`
	}{job})
}
