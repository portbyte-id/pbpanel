package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/apps"
	"github.com/portbyte-id/server-panel/internal/cron"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/httpapi"
	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/logs"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/sites"
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/sysinfo"
)

const (
	// rotateEvery is how often the panel-owned logs are checked against the
	// size threshold.
	rotateEvery = time.Hour

	// Fallbacks used only when a rotation setting cannot be read; they match the
	// registry defaults.
	fallbackRotateBytes = 52428800
	fallbackRotateKeep  = 7

	// sampleErrEvery limits how often a failing host sample is logged, so a host
	// whose /proc cannot be read costs a line per interval of this length rather
	// than one per sample.
	sampleErrEvery = 5 * time.Minute
)

// m7Stack is the dashboard, log viewer and cron features (development plan M7).
// A component that could not be built is nil, and its routes answer 503.
type m7Stack struct {
	Sysinfo httpapi.SysinfoService
	Logs    httpapi.LogsService
	Cron    httpapi.CronService

	sampler *sysinfo.Sampler
	cron    *cron.Service
	rotator *logs.Rotator
}

// sysinfoFacade is the sampler and the service allowlist as one value, which is
// what httpapi.SysinfoService asks for. Their method sets do not overlap.
type sysinfoFacade struct {
	*sysinfo.Sampler
	*sysinfo.Services
}

// siteLister is the part of *sites.Manager the log sources use.
type siteLister interface {
	List(ctx context.Context) ([]sites.Site, error)
}

// newM7 wires the three features. Building touches neither Docker nor the host's
// /proc: a daemon on an odd host still starts, and the failing part says why.
func newM7(st *store.Store, reg *settings.Registry, layout install.Layout, appsSvc httpapi.AppsService, siteList siteLister, logger *slog.Logger) *m7Stack {
	m := &m7Stack{}

	// One Docker client for the three features. docker.New does not dial, so a
	// host without Docker still gets a client whose calls fail.
	var eng *docker.Client
	if c, err := docker.New(docker.DefaultSocket); err != nil {
		logger.Warn("docker is not reachable; container services and logs are off", "error", err)
	} else {
		eng = c
	}

	containers := func(ctx context.Context) ([]string, error) { return panelContainers(ctx, reg), nil }

	var sysApps sysinfo.Apps
	if appsSvc != nil {
		sysApps = sysinfoApps{appsSvc}
	}
	svcOpts := sysinfo.ServicesOptions{Cmd: sysinfo.ExecCommander{}, Containers: containers, Apps: sysApps}
	if eng != nil {
		svcOpts.Engine = eng
	}

	var lastSampleErr time.Time
	sampler, err := sysinfo.New(sysinfo.Options{
		Source: sysinfo.HostSource{},
		OnError: func(err error) {
			if time.Since(lastSampleErr) < sampleErrEvery {
				return
			}
			lastSampleErr = time.Now()
			logger.Warn("host sample failed", "error", err)
		},
	})
	if err != nil {
		logger.Error("host metrics are not available", "error", err)
	} else if services, err := sysinfo.NewServices(svcOpts); err != nil {
		logger.Error("host metrics are not available", "error", err)
	} else {
		m.sampler = sampler
		m.Sysinfo = sysinfoFacade{sampler, services}
	}

	allowShell := func() bool {
		ok, err := reg.Bool(context.Background(), settings.KeyCronAllowShell)
		return err == nil && ok
	}
	cronLogs := filepath.Join(layout.LogsDir(), "cron")
	if svc, err := cron.New(cron.Options{
		Store:      st,
		AllowShell: allowShell,
		LogRoot:    cronLogs,
		ScriptDir:  filepath.Join(layout.DataDir(), "cron"),
	}); err != nil {
		logger.Error("scheduled jobs are not available", "error", err)
	} else {
		m.cron = svc
		m.Cron = svc
	}

	m.Logs = newLogRegistry(eng, reg, layout, appsSvc, siteList, m.cron, cronLogs, logger)

	var hooks []logs.ReopenHook
	if eng != nil {
		hooks = append(hooks, reopenContainerLogs(eng, func(ctx context.Context) []string { return panelContainers(ctx, reg) }))
	}
	ctx := context.Background()
	rot, err := logs.NewRotator(logs.RotateConfig{
		Root:     layout.LogsDir(),
		MaxBytes: int64(intSetting(ctx, reg, settings.KeyLogsRotateMaxBytes, fallbackRotateBytes)),
		Keep:     intSetting(ctx, reg, settings.KeyLogsRotateKeep, fallbackRotateKeep),
		Compress: true,
	}, hooks...)
	if err != nil {
		logger.Error("log rotation is not available", "error", err)
	} else {
		m.rotator = rot
	}
	return m
}

// start runs the sampler, the cron loop and the log rotation until ctx ends. The
// returned function waits for the cron loop to stop its running jobs, for at most
// the grace period.
func (m *m7Stack) start(ctx context.Context, logger *slog.Logger) (wait func(grace time.Duration)) {
	var wg sync.WaitGroup
	if m.sampler != nil {
		wg.Add(1)
		go func() { defer wg.Done(); m.sampler.Run(ctx) }()
	}
	if m.cron != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.cron.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("cron stopped", "error", err)
			}
		}()
	}
	if m.rotator != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.rotator.Run(ctx, rotateEvery, func(err error) { logger.Warn("log rotation", "error", err) })
		}()
	}
	return func(grace time.Duration) {
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(grace):
			logger.Warn("background tasks did not stop within the grace period")
		}
	}
}

// panelContainers names the containers the panel owns: nginx, and one per PHP
// version in the catalogue setting. A setting that cannot be read leaves nginx.
func panelContainers(ctx context.Context, reg *settings.Registry) []string {
	names := []string{nginx.ContainerName}
	csv, err := reg.Str(ctx, settings.KeyPHPVersions)
	if err != nil {
		return names
	}
	versions, err := php.ParseVersions(csv)
	if err != nil {
		return names
	}
	for _, v := range versions {
		names = append(names, php.ContainerName(v))
	}
	return names
}

// sysinfoApps adapts the app service to sysinfo.Apps: its Start, Stop and Restart
// match, its List returns richer rows.
type sysinfoApps struct{ httpapi.AppsService }

func (a sysinfoApps) List(ctx context.Context) ([]sysinfo.AppState, error) {
	list, err := a.AppsService.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]sysinfo.AppState, 0, len(list))
	for _, s := range list {
		out = append(out, sysinfo.AppState{Name: s.Name, State: s.State})
	}
	return out, nil
}

// --- log sources -------------------------------------------------------------

// newLogRegistry registers the panel's logs: the audit trail and supervisord's
// own log as fixed files, and providers for the sources that come and go.
//
// The panel daemon itself writes to the journal (the unit sets StandardOutput=
// journal), so it has no file to show; its log stays in `journalctl -u panel`.
func newLogRegistry(eng *docker.Client, reg *settings.Registry, layout install.Layout, appsSvc httpapi.AppsService, siteList siteLister, cronSvc *cron.Service, cronLogs string, logger *slog.Logger) *logs.Registry {
	var dl logs.DockerLogs
	if eng != nil {
		dl = dockerLogs{eng}
	}
	r := logs.NewRegistry(dl)

	for _, s := range []logs.Source{
		{ID: "panel:audit", Label: "Panel audit trail", Kind: logs.KindAudit, Path: filepath.Join(layout.LogsDir(), "audit.log")},
		{ID: "panel:supervisord", Label: "Supervisord", Kind: logs.KindPanel, Path: filepath.Join(layout.LogsDir(), "supervisord.log")},
	} {
		if err := r.Add(s); err != nil {
			logger.Error("log source not registered", "id", s.ID, "error", err)
		}
	}

	r.AddProvider(logs.ProviderFunc(func(ctx context.Context) ([]logs.Source, error) {
		var out []logs.Source
		for _, name := range panelContainers(ctx, reg) {
			out = append(out, logs.Source{ID: "docker:" + name, Label: name, Kind: logs.KindDocker, Container: name})
		}
		return out, nil
	}))

	root := filepath.ToSlash(layout.Root)
	if siteList != nil {
		r.AddProvider(logs.ProviderFunc(func(ctx context.Context) ([]logs.Source, error) {
			return siteLogSources(ctx, siteList, nginx.Paths{Root: root}, php.Paths{Root: root})
		}))
	}
	if appsSvc != nil {
		r.AddProvider(logs.ProviderFunc(func(ctx context.Context) ([]logs.Source, error) {
			return appLogSources(ctx, appsSvc, apps.Paths{Root: root})
		}))
	}
	if cronSvc != nil {
		r.AddProvider(logs.ProviderFunc(func(ctx context.Context) ([]logs.Source, error) {
			return cronLogSources(ctx, cronSvc, cronLogs)
		}))
	}
	return r
}

// siteLogSources is each site's nginx access and error log, and the PHP error
// log of a PHP site.
func siteLogSources(ctx context.Context, l siteLister, np nginx.Paths, pp php.Paths) ([]logs.Source, error) {
	list, err := l.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("site logs: %w", err)
	}
	out := make([]logs.Source, 0, 3*len(list))
	for _, s := range list {
		out = append(out,
			logs.Source{ID: "site:" + s.Domain + ":access", Label: s.Domain + " access", Kind: logs.KindSite, Path: np.AccessLog(s.Domain)},
			logs.Source{ID: "site:" + s.Domain + ":error", Label: s.Domain + " error", Kind: logs.KindSite, Path: np.ErrorLog(s.Domain)},
		)
		if s.PHPVersion != nil {
			out = append(out, logs.Source{ID: "site:" + s.Domain + ":php", Label: s.Domain + " PHP error", Kind: logs.KindSite, Path: pp.ErrorLog(s.Domain)})
		}
	}
	return out, nil
}

// appLogSources is each app's output log. The supervisor starts every run in
// out.log.<timestamp> and never makes the plain name, so the newest file with
// that prefix is the live one (the same rule the app manager's own log reader
// uses). It is looked up on each listing, so a restarted app is followed from its
// next request, not within one open tail.
func appLogSources(ctx context.Context, a httpapi.AppsService, p apps.Paths) ([]logs.Source, error) {
	list, err := a.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("app logs: %w", err)
	}
	out := make([]logs.Source, 0, len(list))
	for _, s := range list {
		out = append(out, logs.Source{
			ID: "app:" + s.Name, Label: s.Name, Kind: logs.KindApp,
			Path: newestAppLog(p.LogFile(s.Name)),
		})
	}
	return out, nil
}

// newestAppLog returns the newest file in plain's directory whose name starts
// with plain's base name, or plain itself when there is none.
func newestAppLog(plain string) string {
	native := filepath.FromSlash(plain)
	entries, err := os.ReadDir(filepath.Dir(native))
	if err != nil {
		return plain
	}
	var best string
	var bestTime time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), filepath.Base(native)) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best, bestTime = e.Name(), info.ModTime()
		}
	}
	if best == "" {
		return plain
	}
	return filepath.ToSlash(filepath.Join(filepath.Dir(native), best))
}

// cronRunLister is the part of *cron.Service the log sources use.
type cronRunLister interface {
	List(ctx context.Context) ([]cron.Job, error)
	Runs(ctx context.Context, jobID int64, limit int) ([]cron.Run, error)
}

// cronLogSources is each job's newest run log. A path outside the cron log root
// is dropped: the sources are the panel's to publish, and the database is not the
// place that decides what that is.
func cronLogSources(ctx context.Context, c cronRunLister, root string) ([]logs.Source, error) {
	jobs, err := c.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("cron logs: %w", err)
	}
	var out []logs.Source
	for _, j := range jobs {
		runs, err := c.Runs(ctx, j.ID, 1)
		if err != nil || len(runs) == 0 || runs[0].LogPath == "" {
			continue
		}
		rel, err := filepath.Rel(root, runs[0].LogPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		out = append(out, logs.Source{ID: "cron:" + j.Name, Label: j.Name + " (last run)", Kind: logs.KindCron, Path: runs[0].LogPath})
	}
	return out, nil
}

// --- docker logs and log reopening --------------------------------------------

// containerLogReader is the part of *docker.Client the log adapter uses.
type containerLogReader interface {
	ContainerInspect(ctx context.Context, id string) (docker.Container, error)
	ContainerLogs(ctx context.Context, id string, opts docker.LogOptions, stdout, stderr io.Writer) error
}

// dockerLogs adapts the Docker client to logs.DockerLogs. The client copies a
// demultiplexed stream into writers; this turns it into the reader the viewer
// wants, with stdout and stderr interleaved in arrival order.
type dockerLogs struct{ c containerLogReader }

func (d dockerLogs) ContainerLogs(ctx context.Context, name string, opts logs.DockerOptions) (io.ReadCloser, error) {
	// The client reports a missing container only on the stream, after the
	// request; asking first lets the viewer answer with a status instead of a
	// stream that fails.
	if _, err := d.c.ContainerInspect(ctx, name); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	go func() {
		err := d.c.ContainerLogs(ctx, name, docker.LogOptions{Tail: opts.Tail, Follow: opts.Follow}, pw, pw)
		_ = pw.CloseWithError(err) // a nil err reads as io.EOF
	}()
	return &pipeCloser{PipeReader: pr, cancel: cancel}, nil
}

// pipeCloser ends the copy when the reader is closed: it cancels the request and
// unblocks a write waiting on a reader that has gone.
type pipeCloser struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (p *pipeCloser) Close() error {
	p.cancel()
	return p.PipeReader.Close()
}

// containerSignaler is the part of *docker.Client the reopen hook uses.
type containerSignaler interface {
	ContainerInspect(ctx context.Context, id string) (docker.Container, error)
	ContainerKill(ctx context.Context, id, signal string) error
}

// reopenContainerLogs signals every running panel container with USR1, which
// nginx and PHP-FPM both take as "reopen your log files". A container that does
// not exist or is stopped has no file open, so it is skipped. The signal is the
// documented one for both programs; it has not been run against a rotated log on
// a host.
func reopenContainerLogs(c containerSignaler, names func(ctx context.Context) []string) logs.ReopenHook {
	return func(ctx context.Context) error {
		var errs []error
		for _, name := range names(ctx) {
			info, err := c.ContainerInspect(ctx, name)
			if errors.Is(err, docker.ErrNotFound) {
				continue
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				continue
			}
			if !info.State.Running {
				continue
			}
			if err := c.ContainerKill(ctx, name, "USR1"); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			}
		}
		return errors.Join(errs...)
	}
}
