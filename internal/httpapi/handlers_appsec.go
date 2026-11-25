package httpapi

import (
	"context"
	"net/http"

	"github.com/portbyte-id/server-panel/internal/appsec"
	"github.com/portbyte-id/server-panel/internal/jobs"
)

// AppsecService is the open-appsec agent's lifecycle and state. *appsec.Manager
// implements it; tests substitute a fake.
type AppsecService interface {
	Status(ctx context.Context) (appsec.Status, error)
	Availability(ctx context.Context) appsec.Availability
	Install(ctx context.Context, opts appsec.InstallOptions) error
	Uninstall(ctx context.Context, inUse func(context.Context) (bool, error)) error
}

// appsecRoutes registers the Store's open-appsec card and its install and
// uninstall actions. They are registered even when Deps.Appsec is nil, so the
// Store gets a 503 that says why instead of a 404 that looks like a missing
// feature.
func (s *server) appsecRoutes(mux *http.ServeMux) {
	h := func(fn func(AppsecService, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Appsec == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"open-appsec is not available on this host; see the daemon log")
				return
			}
			fn(s.deps.Appsec, w, r)
		}))
	}

	mux.Handle("GET /api/appsec", h(s.handleAppsecStatus))
	mux.Handle("POST /api/appsec/install", h(s.handleAppsecInstall))
	mux.Handle("POST /api/appsec/uninstall", h(s.handleAppsecUninstall))
}

// appsecResponse is the Store card: the availability verdict, the agent's own
// state, and the warning text the interface renders verbatim.
type appsecResponse struct {
	appsec.Availability
	// Job is the job started by this request. It is absent on a read.
	Job *jobs.Job `json:"job,omitempty"`
}

func (s *server) handleAppsecStatus(a AppsecService, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, appsecResponse{Availability: a.Availability(r.Context())})
}

// appsecInstallRequest carries the operator's acknowledgement of the tracking
// warning. The field is a string rather than a boolean on purpose: the client has
// to send back the exact value it read, so an install is never a silent POST.
type appsecInstallRequest struct {
	Acknowledge string `json:"acknowledge"`
}

// handleAppsecInstall starts the install as a job. The acknowledgement is checked
// here, before any work starts, so a request without it is a 422 that names the
// field rather than a job that fails a minute later.
func (s *server) handleAppsecInstall(a AppsecService, w http.ResponseWriter, r *http.Request) {
	var req appsecInstallRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	if req.Acknowledge != appsec.Acknowledgement {
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope{
			Error:  errInvalidInput,
			Field:  "acknowledge",
			Detail: "read the tracking warning and send its acknowledgement back: " + appsec.Warning,
		})
		return
	}
	note(r, "action", "appsec.install")
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), "appsec.install", func(ctx context.Context, out *jobs.Writer) error {
		return a.Install(ctx, appsec.InstallOptions{
			Acknowledge: req.Acknowledge,
			Progress:    func(line string) { _ = out.Line(line) },
		})
	})
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, appsecResponse{Availability: a.Availability(context.WithoutCancel(r.Context())), Job: &job})
}

// handleAppsecUninstall starts the uninstall as a job. Whether a site still uses
// the engine is decided by the manager at uninstall time, not here, so the check
// and the removal cannot be separated by a concurrent firewall change.
func (s *server) handleAppsecUninstall(a AppsecService, w http.ResponseWriter, r *http.Request) {
	note(r, "action", "appsec.uninstall")
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), "appsec.uninstall", func(ctx context.Context, out *jobs.Writer) error {
		return a.Uninstall(ctx, s.deps.WAFEngineInUse)
	})
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, appsecResponse{Availability: a.Availability(context.WithoutCancel(r.Context())), Job: &job})
}
