package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/httpapi"
	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/sites"
	"github.com/portbyte-id/server-panel/internal/store"
)

// nginxStack is everything the nginx and sites features run on. The HTTP routes
// of step 1.10 take it from here.
type nginxStack struct {
	Editor     *confedit.Editor
	Service    *nginx.Service
	Global     *nginx.Global
	Sites      *sites.Manager
	Reconciler *sites.Reconciler
	Paths      nginx.Paths
}

// newNginxStack wires the Docker client, the config editor, the nginx service
// and the sites model. Nothing here touches Docker or the disk: a daemon on a
// host without Docker still starts, and the reconcile reports what is missing.
func newNginxStack(st *store.Store, layout install.Layout, jobsMgr *jobs.Manager, logger *slog.Logger) (*nginxStack, error) {
	paths := nginx.Paths{Root: filepath.ToSlash(layout.Root)}

	eng, err := docker.New(docker.DefaultSocket)
	if err != nil {
		return nil, err
	}
	ed, err := confedit.New(confedit.Options{
		Roots:      []string{paths.Dir()},
		HistoryDir: filepath.Join(layout.PanelDir(), "history"),
		Store:      st,
	})
	if err != nil {
		return nil, fmt.Errorf("config editor: %w", err)
	}

	live := nginx.NewService(eng, paths)
	global := nginx.NewGlobal(st, ed, live, paths)
	mgr, err := sites.New(sites.Options{Store: st, Applier: ed, Service: live, Paths: paths})
	if err != nil {
		return nil, err
	}

	buildImage := func(ctx context.Context) error {
		job, err := jobsMgr.Start(ctx, "nginx.image", func(ctx context.Context, out *jobs.Writer) error {
			return nginx.BuildImage(ctx, eng, func(line string) { _ = out.Line(line) })
		})
		if err != nil {
			return err
		}
		done, err := jobsMgr.Wait(ctx, job.ID)
		if err != nil {
			return err
		}
		if done.State != jobs.StateSucceeded {
			return fmt.Errorf("building %s: job %s %s: %s", nginx.Image, job.ID, done.State, done.Err)
		}
		return nil
	}

	return &nginxStack{
		Editor: ed, Service: live, Global: global, Sites: mgr, Paths: paths,
		Reconciler: &sites.Reconciler{
			Sites: mgr, Global: global,
			Recover: ed.Recover,
			Boot:    nginx.NewValidateOnly(eng, paths),
			Live:    live, Runtime: nginx.NewRuntime(eng, paths),
			BuildImage: buildImage, Logger: logger,
		},
	}, nil
}

// start reconciles nginx in the background. The panel must be reachable when
// nginx is not, because that is when an operator needs it, so a failure is
// logged and kept in the reconciler's status, never fatal.
func (n *nginxStack) start(ctx context.Context, logger *slog.Logger) {
	go func() {
		status := n.Reconciler.Run(ctx)
		if status.OK {
			logger.Info("nginx reconciled", "steps", len(status.Steps))
		}
	}()
}

// deps is the stack as the HTTP layer takes it.
func (n *nginxStack) deps() *httpapi.NginxDeps {
	return &httpapi.NginxDeps{
		Sites: n.Sites, Global: n.Global, History: n.Editor,
		Service: n.Service, Reconciler: n.Reconciler, Paths: n.Paths,
	}
}
