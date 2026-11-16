package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/portbyte-id/server-panel/internal/audit"
	"github.com/portbyte-id/server-panel/internal/auth"
	"github.com/portbyte-id/server-panel/internal/httpapi"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/webui"
	"github.com/portbyte-id/server-panel/web"
)

// Fallbacks used only when a login-policy setting cannot be read. They match
// the registry defaults, so a database hiccup never turns the throttle off.
const (
	fallbackIdle     = 30 * time.Minute
	fallbackAttempts = 5
	fallbackLockout  = 15 * time.Minute

	sweepEvery = time.Hour
	stopGrace  = 15 * time.Second
)

// serve runs the daemon: it opens the store, wires every package into the HTTP
// handler, listens on panel.bind:panel.port and stops cleanly on SIGTERM or
// SIGINT. A port change from the UI rebinds the listener without a restart.
func serve(ctx context.Context, _ []string) error {
	layout := defaultLayout()

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	st, err := store.Open(layout.DBPath())
	if err != nil {
		return fmt.Errorf("serve: open database: %w", err)
	}
	defer func() { _ = st.Close() }()

	reg, err := settings.New(st)
	if err != nil {
		return fmt.Errorf("serve: settings: %w", err)
	}

	// The login policy reads the registry on every call, so a change in the UI
	// applies to the next login without a restart.
	authSvc := auth.NewService(st, auth.SystemClock{},
		func() time.Duration { return durationSetting(ctx, reg, "session.idle_timeout", fallbackIdle) },
		func() int { return intSetting(ctx, reg, "login.max_attempts", fallbackAttempts) },
		func() time.Duration { return durationSetting(ctx, reg, "login.lockout", fallbackLockout) },
	)

	jobsMgr, err := jobs.New(st)
	if err != nil {
		return fmt.Errorf("serve: jobs: %w", err)
	}

	dist, err := web.Dist()
	if err != nil {
		return fmt.Errorf("serve: embedded interface: %w", err)
	}
	ui, err := webui.Handler(dist)
	if err != nil {
		return fmt.Errorf("serve: embedded interface: %w", err)
	}

	paths := httpapi.CertPaths{CertFile: layout.CertPath(), KeyFile: layout.KeyPath()}
	bind, err := reg.Str(ctx, "panel.bind")
	if err != nil {
		return fmt.Errorf("serve: panel.bind: %w", err)
	}
	port, err := reg.Int(ctx, "panel.port")
	if err != nil {
		return fmt.Errorf("serve: panel.port: %w", err)
	}

	// Building the stack touches neither Docker nor the disk; a failure here is
	// a wiring problem, and the nginx routes then answer 503.
	var nginxDeps *httpapi.NginxDeps
	nginxStack, err := newNginxStack(st, layout, jobsMgr, logger)
	if err != nil {
		logger.Error("nginx is not available", "error", err)
		nginxStack = nil
	} else {
		nginxDeps = nginxStack.deps()
	}

	// listener is assigned below; Rebind runs only on requests, which cannot
	// arrive before it is serving.
	var listener *httpapi.Listener
	handler, err := httpapi.New(httpapi.Deps{
		Store:    st,
		Settings: reg,
		Auth:     authSvc,
		Audit: audit.New(st.DB(),
			audit.WithMirrorPath(filepath.Join(layout.LogsDir(), "audit.log")),
			audit.WithLogger(logger)),
		Jobs:  jobsMgr,
		UI:    ui,
		Nginx: nginxDeps,
		Rebind: func(p int) error {
			url, err := listener.ChangePort(p)
			if err == nil {
				logger.Info("panel port changed", "url", url)
			}
			return err
		},
		Logger: logger,
	})
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	listener, err = httpapi.NewListener(handler, paths, bind, httpapi.DefaultGrace)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	if err := listener.Start(port); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	logger.Info("panel listening", "bind", bind, "port", listener.Port())

	go sweepSessions(ctx, authSvc, logger)

	// After the listener: a slow image build or an absent Docker must not keep
	// the operator out of the panel that can show them why.
	if nginxStack != nil {
		nginxStack.start(ctx, logger)
	}

	<-ctx.Done()
	logger.Info("panel stopping")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), stopGrace)
	defer cancel()
	if err := listener.Close(shutdownCtx); err != nil {
		logger.Warn("listener did not stop cleanly", "error", err)
	}
	return nil
}

// sweepSessions deletes expired and revoked sessions once an hour.
func sweepSessions(ctx context.Context, svc *auth.Service, logger *slog.Logger) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := svc.Sessions().Sweep(ctx); err != nil {
				logger.Warn("session sweep failed", "error", err)
			}
		}
	}
}

func durationSetting(ctx context.Context, reg *settings.Registry, name string, fallback time.Duration) time.Duration {
	d, err := reg.Duration(ctx, name)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func intSetting(ctx context.Context, reg *settings.Registry, name string, fallback int) int {
	n, err := reg.Int(ctx, name)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
