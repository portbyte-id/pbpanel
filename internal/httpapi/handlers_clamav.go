package httpapi

import (
	"context"
	"net/http"

	"github.com/portbyte-id/server-panel/internal/clamav"
	"github.com/portbyte-id/server-panel/internal/jobs"
)

// ClamavService is ClamAV's lifecycle and scanner. *clamav.Manager implements
// it; tests substitute a fake.
type ClamavService interface {
	Availability(ctx context.Context) clamav.Availability
	Install(ctx context.Context, progress func(string)) error
	Uninstall(ctx context.Context, progress func(string)) error
	Scan(ctx context.Context, path string, progress func(string)) (clamav.ScanResult, error)
}

// clamavRoutes registers the Store's ClamAV card and the scan action. They are
// registered even when Deps.Clamav is nil so the Store gets a 503 that says why.
func (s *server) clamavRoutes(mux *http.ServeMux) {
	h := func(fn func(ClamavService, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Clamav == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"ClamAV is not available on this host; see the daemon log")
				return
			}
			fn(s.deps.Clamav, w, r)
		}))
	}
	mux.Handle("GET /api/clamav", h(s.handleClamavStatus))
	mux.Handle("POST /api/clamav/install", h(s.handleClamavInstall))
	mux.Handle("POST /api/clamav/uninstall", h(s.handleClamavUninstall))
	mux.Handle("POST /api/clamav/scan", h(s.handleClamavScan))
}

// clamavResponse is the Store card; Job is set on the actions that start one.
type clamavResponse struct {
	clamav.Availability
	Job *jobs.Job `json:"job,omitempty"`
}

func (s *server) handleClamavStatus(c ClamavService, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, clamavResponse{Availability: c.Availability(r.Context())})
}

func (s *server) clamavJob(c ClamavService, w http.ResponseWriter, r *http.Request, kind string,
	fn func(ctx context.Context, progress func(string)) error) {
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), kind, func(ctx context.Context, out *jobs.Writer) error {
		return fn(ctx, func(line string) { _ = out.Line(line) })
	})
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, clamavResponse{Availability: c.Availability(context.WithoutCancel(r.Context())), Job: &job})
}

func (s *server) handleClamavInstall(c ClamavService, w http.ResponseWriter, r *http.Request) {
	note(r, "action", "clamav.install")
	s.clamavJob(c, w, r, "clamav.install", c.Install)
}

func (s *server) handleClamavUninstall(c ClamavService, w http.ResponseWriter, r *http.Request) {
	note(r, "action", "clamav.uninstall")
	s.clamavJob(c, w, r, "clamav.uninstall", c.Uninstall)
}

// handleClamavScan scans a file or folder as a job. The path goes through the
// file manager's own validation first, so a scan can reach exactly what the file
// manager can, and a missing path is a 404 now rather than a failed job later.
// A scan that finds infected files fails the job, with the files in its output.
func (s *server) handleClamavScan(c ClamavService, w http.ResponseWriter, r *http.Request) {
	if s.deps.Files == nil {
		writeError(w, http.StatusServiceUnavailable, errUnavailable, "the file manager is not available on this host")
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if !s.fmBody(w, r, &body) {
		return
	}
	clean, ok := s.fmPath(s.deps.Files, w, r, body.Path)
	if !ok {
		return
	}
	if _, err := s.deps.Files.Stat(clean); err != nil {
		s.writeFMError(w, r, err)
		return
	}
	a := c.Availability(r.Context())
	if !a.Installed {
		writeError(w, http.StatusConflict, errConflict, clamav.ErrNotInstalled.Error())
		return
	}
	if a.Scanning {
		writeError(w, http.StatusConflict, errConflict, clamav.ErrScanBusy.Error())
		return
	}
	note(r, "action", "clamav.scan")
	noteTarget(r, clean)
	s.clamavJob(c, w, r, "clamav.scan", func(ctx context.Context, progress func(string)) error {
		_, err := c.Scan(ctx, clean, progress)
		return err
	})
}
