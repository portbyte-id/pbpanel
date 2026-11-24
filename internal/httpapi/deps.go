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

	// Firewall is the iptables firewall. Nil leaves /api/firewall answering 503.
	Firewall FirewallService

	// Apps runs Go, Node.js and command apps under supervisord. Nil leaves
	// /api/apps answering 503, which is what a host without supervisord looks like.
	Apps AppsService

	// Runtimes installs Node.js versions. Nil leaves /api/runtimes answering 503.
	Runtimes RuntimesService

	// TLS carries certificates for sites and the panel. Nil leaves the TLS
	// routes answering 503.
	TLS *TLSDeps

	// Sysinfo is the host sampler and the service allowlist. Nil leaves
	// /api/sysinfo and /api/services answering 503.
	Sysinfo SysinfoService

	// Logs is the log viewer. Nil leaves /api/logs answering 503.
	Logs LogsService

	// Cron is the job scheduler. Nil leaves /api/cron answering 503.
	Cron CronService

	// WAFMon is the traffic monitor and WAF event reader. Nil leaves
	// /api/waf/traffic, /requests and /events answering 503.
	WAFMon WAFMonitor

	// Geo is the country database behind the world map. Nil leaves the map
	// empty and the download answering 503.
	Geo GeoSource

	// SSHAuth is the SSH login-attempt monitor over the sshd journal. Nil leaves
	// /api/ssh/attempts answering 503.
	SSHAuth SSHAuthMonitor

	// Docker is the Docker page: listings, resource monitor and container
	// controls. Nil leaves /api/docker answering 503.
	Docker DockerService

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
