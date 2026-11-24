package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/guard"
	"github.com/portbyte-id/server-panel/internal/httpapi"
	"github.com/portbyte-id/server-panel/internal/images"
	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/sites"
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/waf"
	"github.com/portbyte-id/server-panel/internal/webroots"
)

// wafAvailability says whether ModSecurity can run: every nginx build ships the
// module and the Core Rule Set, so it can as soon as a build serves.
func wafAvailability(built bool) waf.Availability {
	if !built {
		return waf.Availability{Flavor: "pqc", Reason: "no nginx build serves yet; build one on the Nginx page"}
	}
	return waf.Availability{Available: true, Flavor: "pqc", CRSVersion: nginx.CRSVersion}
}

// openAppsecStatus reports whether the open-appsec agent is installed and
// running: the control script exists and its unit is active. Step 2 owns the
// install, the version and the Store card; this is the minimum a firewall change
// needs to refuse loading a module whose agent is not there, and it is read on
// every use so an agent installed later applies without a panel restart.
func openAppsecStatus(run nginx.Runner) func(ctx context.Context) (bool, string) {
	return func(ctx context.Context) (bool, string) {
		if _, err := os.Stat(waf.OpenAppsecCtl); err != nil {
			return false, "the open-appsec agent is not installed"
		}
		out, err := run.Run(ctx, "systemctl", "is-active", waf.OpenAppsecUnit)
		state := strings.TrimSpace(string(out))
		if err != nil || state != "active" {
			if state == "" {
				state = "unknown"
			}
			return false, fmt.Sprintf("%s is %s", waf.OpenAppsecUnit, state)
		}
		return true, ""
	}
}

// guardKeys builds the challenge page's keyring from data/secret.key.
func guardKeys(layout install.Layout) (*guard.Keyring, error) {
	master, err := os.ReadFile(layout.SecretKeyPath())
	if err != nil {
		return nil, fmt.Errorf("reading the secret key: %w", err)
	}
	return guard.NewKeyring(master)
}

// turnstileKeys is the firewall's source of the panel's Turnstile keys. They are
// Settings, read on every use, so a key typed in the Settings page applies to the
// next visitor and the next save without a reload.
func turnstileKeys(reg *settings.Registry) waf.TurnstileSource {
	return func(ctx context.Context) (siteKey, secret string, err error) {
		if siteKey, err = reg.Str(ctx, settings.KeyWAFTurnstileSiteKey); err != nil {
			return "", "", fmt.Errorf("reading the Turnstile site key: %w", err)
		}
		if secret, err = reg.Str(ctx, settings.KeyWAFTurnstileSecret); err != nil {
			return "", "", fmt.Errorf("reading the Turnstile secret key: %w", err)
		}
		return siteKey, secret, nil
	}
}

// wwwGroupID is the group of the account nginx's workers run as. The socket must
// be group-owned by it so the workers can connect. The numeric WWWGID is only a
// default for a host where that account does not exist yet: a host whose www
// account has another gid (1000 on some VMs) would otherwise refuse the workers
// with EACCES, and every challenge request would answer 502.
func wwwGroupID(lookup func(string) (*user.User, error)) int {
	u, err := lookup(nginx.WWWUser)
	if err != nil {
		return nginx.WWWGID
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nginx.WWWGID
	}
	return gid
}

// serveGuard answers the challenge page over the unix socket nginx proxies
// /.pb-challenge/ to. A site whose page cannot reach it shows the visitor an
// error and a retry button; visitors with a valid cookie are not affected.
func serveGuard(ctx context.Context, sock string, h *guard.Handler, logger *slog.Logger) {
	if err := os.MkdirAll(filepath.Dir(filepath.FromSlash(sock)), 0o755); err != nil {
		logger.Error("the challenge socket directory could not be created", "error", err)
		return
	}
	ln, err := guard.Listen(filepath.FromSlash(sock), wwwGroupID(user.Lookup))
	if err != nil {
		logger.Error("the challenge page cannot be answered", "error", err)
		return
	}
	logger.Info("challenge socket listening", "path", sock)
	if err := guard.Serve(ctx, ln, h); err != nil {
		logger.Error("the challenge socket stopped", "error", err)
	}
}

// legacyNginxContainer is the container nginx ran in before it ran under systemd.
const legacyNginxContainer = "panel-nginx"

// containerRemover is the part of the Docker client the retirement uses.
type containerRemover interface {
	ContainerInspect(ctx context.Context, id string) (docker.Container, error)
	ContainerStop(ctx context.Context, id string, timeout time.Duration) error
	ContainerRemove(ctx context.Context, id string, force bool) error
}

// retireNginxContainer stops and removes the panel-nginx container a host
// upgraded from the container runtime still has, so the native nginx can bind
// its ports. Only a container the panel made is touched: one without the managed
// label is somebody else's, and keeping it is the right answer to a port clash.
// A host without Docker, or without the container, has nothing to retire.
func retireNginxContainer(ctx context.Context, eng containerRemover, logger *slog.Logger) error {
	c, err := eng.ContainerInspect(ctx, legacyNginxContainer)
	if errors.Is(err, docker.ErrNotFound) {
		return nil
	}
	if err != nil {
		// Docker not answering means no container is holding the ports either.
		logger.Warn("nginx: could not look for the old container", "error", err)
		return nil
	}
	if c.Labels[docker.LabelManaged] != "true" {
		return fmt.Errorf("a container named %s exists but the panel did not make it; remove or rename it", legacyNginxContainer)
	}
	logger.Info("nginx: removing the container it ran in before; it now runs under systemd")
	if err := eng.ContainerStop(ctx, legacyNginxContainer, 25*time.Second); err != nil {
		return err
	}
	return eng.ContainerRemove(ctx, legacyNginxContainer, true)
}

// legacyPHPContainer is the container one PHP version ran in before it ran
// under systemd. Only the upgrade path knows this name now: nothing creates
// such a container any more.
func legacyPHPContainer(ver string) string { return "panel-php-" + ver }

// retirePHPContainer stops and removes the panel-php-<ver> container a host
// upgraded from the container PHP runtime still has, right before the version's
// first native start: config files and pool sockets are the same paths, so the
// new FPM master simply recreates them. Only a container the panel made is
// touched; a foreign one means the socket or the sites tree is somebody else's
// to keep running. A host without Docker, or without the container, has nothing
// to retire. Old panel/php:* images are left on disk for the operator to remove.
func retirePHPContainer(ctx context.Context, ver string, eng containerRemover, logger *slog.Logger) error {
	name := legacyPHPContainer(ver)
	c, err := eng.ContainerInspect(ctx, name)
	if errors.Is(err, docker.ErrNotFound) {
		return nil
	}
	if err != nil {
		// Docker not answering means no container is holding the sockets either.
		logger.Warn("php: could not look for the old container", "version", ver, "error", err)
		return nil
	}
	if c.Labels[docker.LabelManaged] != "true" {
		return fmt.Errorf("a container named %s exists but the panel did not make it; remove or rename it", name)
	}
	logger.Info("php: removing the container it ran in before; it now runs under systemd", "version", ver)
	if err := eng.ContainerStop(ctx, name, 25*time.Second); err != nil {
		return err
	}
	return eng.ContainerRemove(ctx, name, true)
}

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
	// Guard answers the challenge page; nil without data/secret.key.
	Guard *guard.Handler
	// Prebuilt reports whether the next nginx build may take a CI-built package.
	Prebuilt func(ctx context.Context) bool

	phpMu    sync.Mutex
	phpSteps []sites.PHPStep
}

// newNginxStack wires the Docker client, the config editor, the nginx service
// and the sites model. Nothing here touches Docker, systemd or the disk: a daemon
// on a host without Docker still starts, and the reconcile reports what is
// missing. Docker is only the builder of nginx and PHP; both run natively under
// systemd.
func newNginxStack(st *store.Store, reg *settings.Registry, layout install.Layout, jobsMgr *jobs.Manager, ed *confedit.Editor, tlsState sites.TLSState, logger *slog.Logger) (*nginxStack, error) {
	// The allowed web roots are one holder both services read, so the Settings
	// page changes what the next spec and render see.
	roots := webroots.NewHolder()
	paths := nginx.Paths{Root: filepath.ToSlash(layout.Root), Roots: roots}
	phpPaths := php.Paths{Root: paths.Root, Roots: roots, DataDir: filepath.ToSlash(layout.DataDir())}

	eng, err := docker.New(docker.DefaultSocket)
	if err != nil {
		return nil, err
	}
	run := nginx.ExecRunner{}
	// A build takes a package CI already compiled when the operator turned that on
	// and one fits this host; otherwise Docker compiles it here. Both settings are
	// read per build, so the Settings page applies to the next one.
	prebuiltOn := func(ctx context.Context) bool {
		on, err := reg.Bool(ctx, settings.KeyRuntimesPrebuilt)
		return err == nil && on
	}
	builder := &nginx.Prebuilt{
		Fallback: nginx.DockerBuilder{Eng: eng, P: paths},
		P:        paths,
		Enabled:  prebuiltOn,
		URL: func(ctx context.Context) string {
			u, err := reg.Str(ctx, settings.KeyRuntimesURL)
			if err != nil {
				return ""
			}
			return u
		},
	}
	imgs, err := nginx.NewImages(paths, run, builder, images.NewCatalog(st), logger)
	if err != nil {
		return nil, err
	}
	live := nginx.NewService(paths, run)
	global := nginx.NewGlobal(st, ed, live, paths)
	phpMgr, err := php.New(php.Options{
		Store: st, Engine: eng, Applier: ed, Paths: phpPaths, Run: run, Log: logger,
		Supported: func(ctx context.Context) ([]string, error) {
			csv, err := reg.Str(ctx, settings.KeyPHPVersions)
			if err != nil {
				return nil, err
			}
			return php.ParseVersions(csv)
		},
		Retire: func(ctx context.Context, ver string) error {
			return retirePHPContainer(ctx, ver, eng, logger)
		},
	})
	if err != nil {
		return nil, err
	}
	wafEngine := waf.New(st, paths, func(ctx context.Context) (waf.Availability, error) {
		built, err := imgs.Present(ctx)
		if err != nil {
			return waf.Availability{}, err
		}
		return wafAvailability(built), nil
	})
	// The challenge page signs its cookies with keys derived from secret.key.
	// Without the file the rest of the firewall still works.
	var guardHandler *guard.Handler
	if keys, err := guardKeys(layout); err != nil {
		logger.Warn("the challenge page is not available", "error", err)
	} else {
		wafEngine.WithGuard(keys)
		guardHandler = guard.NewHandler(keys, wafEngine.GuardSettings, nil, logger)
	}
	// Turnstile's keys are panel settings: without this source no site can use
	// Turnstile, however the Settings page is filled in.
	wafEngine.WithTurnstile(turnstileKeys(reg))
	// The open-appsec agent is a config service like nginx: a change writes the
	// policy and reloads nginx, then hands the policy to the agent. Step 2 owns
	// installing it and reporting whether it runs; until it lands, the install
	// script is the only way one exists, and a host without it simply cannot
	// switch a site to the engine.
	appsec := waf.NewOpenAppsecService(run, wafEngine.OpenAppsecPolicyFile(), openAppsecStatus(run))
	mgr, err := sites.New(sites.Options{
		Store: st, Applier: ed, Service: live, Paths: paths, PHP: phpMgr, TLS: tlsState,
		WAF: wafEngine, Appsec: appsec,
		Settings: func(ctx context.Context) ([]confedit.File, error) {
			s, err := global.Settings(ctx)
			if err != nil {
				return nil, err
			}
			return global.SettingsFiles(s)
		},
	})
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
	// the build catalogue.
	runtime := nginx.NewRuntime(paths, run).WithFlags(images.NewCatalog(st)).WithRetire(func(ctx context.Context) error {
		return retireNginxContainer(ctx, eng, logger)
	})

	// The list is read now and on every change. nginx reads host paths directly, so
	// only the PHP containers, which mount the roots, are recreated; that runs as a
	// job so the save does not wait for Docker.
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
			_ = out.Line("rewriting the PHP units to allow the new web roots, and restarting the versions that run")
			return phpMgr.ApplyUnits(ctx)
		})
		if err != nil {
			logger.Error("web roots: start the apply job", "error", err)
		}
	})

	return &nginxStack{
		Editor: ed, Service: live, Global: global, Sites: mgr, Paths: paths, PHP: phpMgr, Images: imgs, Runtime: runtime,
		Guard: guardHandler, Prebuilt: prebuiltOn,
		Reconciler: &sites.Reconciler{
			Sites: mgr, Global: global,
			Recover: ed.Recover,
			Boot:    nginx.NewValidateOnly(paths, run),
			Live:    live, Runtime: runtime,
			BuildImage: buildImage, Logger: logger,
		},
	}, nil
}

// start reconciles nginx in the background. The panel must be reachable when
// nginx is not, because that is when an operator needs it, so a failure is
// logged and kept in the reconciler's status, never fatal.
func (n *nginxStack) start(ctx context.Context, logger *slog.Logger) {
	if n.Guard != nil {
		go serveGuard(ctx, n.Paths.GuardSocket(), n.Guard, logger)
	}
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
		Prebuilt: n.Prebuilt,
	}
}
