package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/portbyte-id/server-panel/internal/apps"
	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/httpapi"
	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
	"github.com/portbyte-id/server-panel/internal/runtimes"
	"github.com/portbyte-id/server-panel/internal/store"
)

// newEditor builds the one config editor every feature shares: nginx, PHP and the
// supervisor's program files. They share it because they share the journal under
// /www/panel/history, and an editor that did not list a file's directory among
// its roots would refuse to replay an unfinished apply that wrote there.
func newEditor(st *store.Store, layout install.Layout) (*confedit.Editor, error) {
	root := filepath.ToSlash(layout.Root)
	ed, err := confedit.New(confedit.Options{
		Roots: []string{
			nginx.Paths{Root: root}.Dir(),
			php.Paths{Root: root}.Base(),
			apps.Paths{Root: root}.ConfDir(),
		},
		HistoryDir: filepath.Join(layout.PanelDir(), "history"),
		Store:      st,
	})
	if err != nil {
		return nil, fmt.Errorf("config editor: %w", err)
	}
	return ed, nil
}

// newApps builds the apps and runtimes services. Building them touches neither
// supervisord nor the disk, so a host where supervisord is not running still
// starts; the apps routes then answer 503 from the first call that needs it.
func newApps(st *store.Store, ed *confedit.Editor, layout install.Layout, logger *slog.Logger) (httpapi.AppsService, httpapi.RuntimesService) {
	root := filepath.ToSlash(layout.Root)
	paths := apps.Paths{Root: root}

	// Each needs the other: an app resolves its node binary through the runtimes,
	// and the runtimes refuse to remove a version an app uses. Both closures run
	// on requests, which cannot arrive before the wiring is done.
	var (
		rt  *runtimes.Manager
		mgr *apps.Manager
	)
	rt = runtimes.New(runtimes.Options{
		Paths: runtimes.Paths{Root: root},
		Users: func(ctx context.Context) (map[string][]string, error) {
			if mgr == nil {
				return nil, nil
			}
			return mgr.UsingNode(ctx, rt.Resolve)
		},
	})

	m, err := apps.New(apps.Options{
		Store: st, Applier: ed, Paths: paths, Node: rt,
		Supervisor:  apps.NewClient(filepath.Join(layout.SupervisorDir(), "supervisord.sock")),
		PrepareDirs: prepareAppDir,
	})
	if err != nil {
		logger.Error("apps are not available", "error", err)
		return nil, rt
	}
	mgr = m
	return mgr, rt
}
