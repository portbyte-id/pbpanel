package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/httpapi"
	"github.com/portbyte-id/server-panel/internal/images"
	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/sites"
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/webroots"
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
	PHP        *php.Manager
	// Images is the nginx image family: builds, the one that serves, rollback.
	Images *nginx.Images
	// Runtime starts, stops and restarts the container.
	Runtime *nginx.Runtime

	phpMu    sync.Mutex
	phpSteps []sites.PHPStep
}

// newNginxStack wires the Docker client, the config editor, the nginx service
// and the sites model. Nothing here touches Docker or the disk: a daemon on a
// host without Docker still starts, and the reconcile reports what is missing.
func newNginxStack(st *store.Store, reg *settings.Registry, layout install.Layout, jobsMgr *jobs.Manager, ed *confedit.Editor, tlsState sites.TLSState, logger *slog.Logger) (*nginxStack, error) {
	// The allowed web roots are one holder both services read, so the Settings
	// page changes what the next spec and render see.
	roots := webroots.NewHolder()
	paths := nginx.Paths{Root: filepath.ToSlash(layout.Root), Roots: roots}
	phpPaths := php.Paths{Root: paths.Root, Roots: roots}

	eng, err := docker.New(docker.DefaultSocket)
	if err != nil {
		return nil, err
	}
	imgs, err := nginx.NewImages(eng, paths, images.NewCatalog(st), logger)
	if err != nil {
		return nil, err
	}
	live := nginx.NewService(eng, paths, imgs.ImageFunc())
	global := nginx.NewGlobal(st, ed, live, paths)
	phpMgr, err := php.New(php.Options{
		Store: st, Engine: eng, Applier: ed, Paths: phpPaths, Log: logger,
		Supported: func(ctx context.Context) ([]string, error) {
			csv, err := reg.Str(ctx, settings.KeyPHPVersions)
			if err != nil {
				return nil, err
			}
			return php.ParseVersions(csv)
		},
	})
	if err != nil {
		return nil, err
	}
	mgr, err := sites.New(sites.Options{Store: st, Applier: ed, Service: live, Paths: paths, PHP: phpMgr, TLS: tlsState})
	if err != nil {
		return nil, err
	}

	buildImage := func(ctx context.Context) error {
		job, err := jobsMgr.Start(ctx, "nginx.image", func(ctx context.Context, out *jobs.Writer) error {
			_, err := imgs.Build(ctx, func(line string) { _ = out.Line(line) })
			return err
		})
		if err != nil {
			return err
		}
		done, err := jobsMgr.Wait(ctx, job.ID)
		if err != nil {
			return err
		}
		if done.State != jobs.StateSucceeded {
			return fmt.Errorf("building the nginx image: job %s %s: %s", job.ID, done.State, done.Err)
		}
		return nil
	}

	// A stop made from the panel survives a panel restart: the flag lives beside
	// the image catalogue.
	runtime := nginx.NewRuntime(eng, paths, imgs.ImageFunc()).WithFlags(images.NewCatalog(st))

	// The list is read now and on every change. A change recreates the running
	// containers, because the roots are mounts; that runs as a job so the save
	// does not wait for Docker.
	if saved, err := reg.Str(context.Background(), settings.KeySitesWebRoots); err != nil {
		logger.Error("web roots: read the setting", "error", err)
	} else if list, err := webroots.Parse(saved, paths.Root); err != nil {
		logger.Error("web roots: the saved list is not usable, so none are allowed", "error", err)
	} else {
		roots.Set(list)
	}
	reg.Subscribe(func(c settings.Change) {
		if c.Name != settings.KeySitesWebRoots {
			return
		}
		list, err := webroots.Parse(c.New, paths.Root)
		if err != nil {
			logger.Error("web roots: refusing the new list", "error", err)
			return
		}
		roots.Set(list)
		_, err = jobsMgr.Start(context.Background(), "sites.web_roots", func(ctx context.Context, out *jobs.Writer) error {
			_ = out.Line("recreating running nginx and PHP containers to mount the allowed web roots")
			if err := runtime.ApplyMounts(ctx); err != nil {
				return err
			}
			return phpMgr.ApplyMounts(ctx)
		})
		if err != nil {
			logger.Error("web roots: start the apply job", "error", err)
		}
	})

	return &nginxStack{
		Editor: ed, Service: live, Global: global, Sites: mgr, Paths: paths, PHP: phpMgr, Images: imgs, Runtime: runtime,
		Reconciler: &sites.Reconciler{
			Sites: mgr, Global: global,
			Recover: ed.Recover,
			Boot:    nginx.NewValidateOnly(eng, paths, imgs.ImageFunc()),
			Live:    live, Runtime: runtime,
			BuildImage: buildImage, Logger: logger,
		},
	}, nil
}

// start reconciles nginx in the background. The panel must be reachable when
// nginx is not, because that is when an operator needs it, so a failure is
// logged and kept in the reconciler's status, never fatal.
func (n *nginxStack) start(ctx context.Context, logger *slog.Logger) {
	go func() {
		// The image catalogue first: an image from before builds had IDs is adopted
		// here, and a build the last daemon left running is failed. Without this a
		// reconcile could find no build and start a second one.
		if _, err := n.Images.Reconcile(ctx); err != nil {
			logger.Error("nginx image catalogue reconcile failed", "error", err)
		}
		status := n.Reconciler.Run(ctx)
		if !status.OK {
			// Without Docker, or with confedit's journal not replayed, PHP
			// cannot be reconciled either; the nginx status says why.
			return
		}
		logger.Info("nginx reconciled", "steps", len(status.Steps))

		// PHP after nginx: the nginx run has replayed any unfinished apply.
		if err := n.PHP.Reconcile(ctx); err != nil {
			logger.Error("php catalogue reconcile failed", "error", err)
			return
		}
		steps := n.Sites.ReconcilePHP(ctx)
		for _, s := range steps {
			if s.OK {
				logger.Info("php reconciled", "version", s.Version, "sites", s.Sites, "detail", s.Detail)
			} else {
				logger.Error("php reconcile failed", "version", s.Version, "sites", s.Sites, "error", s.Detail)
			}
		}
		n.phpMu.Lock()
		n.phpSteps = steps
		n.phpMu.Unlock()
	}()
}

// phpReconcile is how each PHP version came out of the boot reconcile.
func (n *nginxStack) phpReconcile() []sites.PHPStep {
	n.phpMu.Lock()
	defer n.phpMu.Unlock()
	return append([]sites.PHPStep(nil), n.phpSteps...)
}

// deps is the stack as the HTTP layer takes it.
func (n *nginxStack) deps() *httpapi.NginxDeps {
	return &httpapi.NginxDeps{
		Sites: n.Sites, Global: n.Global, History: n.Editor,
		Service: n.Service, Reconciler: n.Reconciler, Paths: n.Paths,
		PHP: n.PHP, PHPReconcile: n.phpReconcile, Images: n.Images, Container: n.Runtime,
	}
}
