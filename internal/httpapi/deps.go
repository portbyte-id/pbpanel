package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/portbyte-id/server-panel/internal/audit"
	"github.com/portbyte-id/server-panel/internal/auth"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/store"
)

// defaultActor is the account name recorded when Deps does not name one. The
// panel has exactly one admin (decision 3), so there is one name to record.
const defaultActor = "admin"

// AuditWriter is the audit package's write path, narrowed to the one method the
// middleware calls. *audit.Writer implements it; tests substitute a recorder.
type AuditWriter interface {
	Write(ctx context.Context, e audit.Entry) error
}

// Deps is everything the HTTP layer is given. It is a value, built once by the
// caller and never mutated, which is what keeps handlers free of globals.
type Deps struct {
	Store    *store.Store
	Settings *settings.Registry
	Auth     *auth.Service
	Audit    AuditWriter
	Jobs     *jobs.Manager
	// UI serves the embedded single-page app. Nil leaves the panel API-only,
	// which is what the httptest suites want: they assert on JSON, not HTML.
	UI     http.Handler
	Logger *slog.Logger

	// Rebind moves the live listener to a new port and returns only after the
	// new one is serving; an error means nothing changed. Nil disables live
	// port changes (the httptest suites, which have no listener).
	Rebind func(port int) error

	// Nginx carries the nginx and sites features. Nil leaves their routes
	// answering 503, which is what a host without Docker looks like.
	Nginx *NginxDeps

	// Files is the file manager. Nil leaves /api/fm answering 503.
	Files FileManager

	// Actor is the single admin account's username, recorded in /api/me and as
	// the audit actor. Empty means defaultActor.
	Actor string
}

// validate fills in the optional fields and reports a dependency the server
// cannot run without. A missing one is a wiring mistake, so it fails at
// construction rather than on the first request that needs it.
func (d *Deps) validate() error {
	switch {
	case d.Store == nil:
		return errors.New("httpapi: Deps.Store is required")
	case d.Settings == nil:
		return errors.New("httpapi: Deps.Settings is required")
	case d.Auth == nil:
		return errors.New("httpapi: Deps.Auth is required")
	case d.Audit == nil:
		return errors.New("httpapi: Deps.Audit is required")
	case d.Jobs == nil:
		return errors.New("httpapi: Deps.Jobs is required")
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Actor == "" {
		d.Actor = defaultActor
	}
	return nil
}
