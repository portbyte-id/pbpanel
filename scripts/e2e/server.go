//go:build ignore

// Command e2e-server boots a real panel for the Playwright journey.
//
// It is a throwaway: it exists so the browser test drives the actual HTTP chain,
// the actual settings registry and the actual embedded SPA rather than a mock.
// Build tag `ignore` keeps it out of `go build ./...` and out of the shipped
// binary.
//
//	go run scripts/e2e/server.go -addr 127.0.0.1:18443 -password e2e-password-123
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
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

func main() {
	addr := flag.String("addr", "127.0.0.1:18443", "listen address")
	password := flag.String("password", "e2e-password-123", "admin password to seed")
	dataDir := flag.String("data", "", "data directory (default: a temp dir)")
	flag.Parse()

	if err := run(*addr, *password, *dataDir); err != nil {
		fmt.Fprintln(os.Stderr, "e2e-server:", err)
		os.Exit(1)
	}
}

func run(addr, password, dataDir string) error {
	if dataDir == "" {
		dir, err := os.MkdirTemp("", "panel-e2e-")
		if err != nil {
			return err
		}
		dataDir = dir
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return err
	}

	st, err := store.Open(filepath.Join(dataDir, "panel.db"))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	reg, err := settings.New(st)
	if err != nil {
		return err
	}

	authSvc := auth.NewService(st, auth.SystemClock{},
		func() time.Duration { return 30 * time.Minute },
		func() int { return 5 },
		func() time.Duration { return 15 * time.Minute },
	)
	if err := authSvc.SetPassword(context.Background(), "admin", password); err != nil {
		return err
	}

	jobsMgr, err := jobs.New(st)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	dist, err := web.Dist()
	if err != nil {
		return err
	}
	ui, err := webui.Handler(dist)
	if err != nil {
		return err
	}

	handler, err := httpapi.New(httpapi.Deps{
		Store:    st,
		Settings: reg,
		Auth:     authSvc,
		Audit:    audit.New(st.DB(), audit.WithLogger(logger)),
		Jobs:     jobsMgr,
		UI:       ui,
		Logger:   logger,
	})
	if err != nil {
		return err
	}

	paths := httpapi.DefaultCertPaths(filepath.Join(dataDir, "ssl"))
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if err := httpapi.GenerateSelfSigned(paths, []string{host}); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// A job to stream in the drawer, so the journey can prove the SSE path too.
	if _, err := jobsMgr.Start(ctx, "e2e.demo", func(_ context.Context, out *jobs.Writer) error {
		for i := 1; i <= 3; i++ {
			if err := out.Printf("demo line %d", i); err != nil {
				return err
			}
			time.Sleep(50 * time.Millisecond)
		}
		return nil
	}); err != nil {
		return err
	}

	srv, err := httpapi.NewServer(ctx, reg, paths, handler)
	if err != nil {
		return err
	}
	srv.Addr = addr

	fmt.Printf("panel listening on https://%s\n", addr)
	if err := srv.ListenAndServeTLS("", ""); err != nil && !httpapi.IsClosed(err) {
		return err
	}
	return nil
}
