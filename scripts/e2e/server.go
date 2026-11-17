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
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/apps"
	"github.com/portbyte-id/server-panel/internal/audit"
	"github.com/portbyte-id/server-panel/internal/auth"
	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/httpapi"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/sites"
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

// --- a stand-in for nginx ------------------------------------------------------
//
// The journey needs sites and a config that can be rejected, and the machine
// running it may have no Docker. So the HTTP layer and the sites model are the
// real ones, and what sits behind them is this: files in memory, and a
// "validator" that refuses any file containing `bogus_directive` with a message
// shaped like nginx's. What real nginx does with a config is covered by the
// integration test in internal/sites and by scripts/smoke/1-nginx-sites.sh.

type memFS struct {
	mu    sync.Mutex
	files map[string][]byte
	dirs  map[string]bool
}

type memInfo struct{ fs.FileInfo }

func (m *memFS) ReadFile(n string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.files[n]; ok {
		return b, nil
	}
	return nil, &fs.PathError{Op: "open", Path: n, Err: fs.ErrNotExist}
}

func (m *memFS) Stat(n string) (fs.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[n]; ok || m.dirs[n] {
		return memInfo{}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: n, Err: fs.ErrNotExist}
}

func (m *memFS) MkdirAll(n string, _ fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dirs[n] = true
	return nil
}

func (m *memFS) WriteFile(n string, d []byte, _ fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[n] = d
	return nil
}

func (m *memFS) Remove(n string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, n)
	delete(m.dirs, n)
	return nil
}

func (m *memFS) Chown(string, int, int) error { return nil }

// fakeNginx is the applier, the history, the service and the reconcile status.
type fakeNginx struct {
	fs *memFS

	mu       sync.Mutex
	versions map[string][]confedit.Version
	content  map[string][]byte
}

const brokenMarker = "bogus_directive"

func (f *fakeNginx) Apply(_ context.Context, ch confedit.Change) (confedit.Result, error) {
	for _, file := range ch.Files {
		if file.Delete {
			continue
		}
		for i, line := range strings.Split(string(file.Content), "\n") {
			if strings.Contains(line, brokenMarker) {
				return confedit.Result{}, &confedit.ApplyError{
					Stage: confedit.StageValidate, Service: "nginx",
					Output: fmt.Sprintf("nginx: [emerg] unknown directive %q in %s:%d\nnginx: configuration file /www/panel/nginx/nginx.conf test failed\n",
						brokenMarker, file.Path, i+1),
					Err: errors.New("nginx -t rejected the configuration (exit 1)"),
				}
			}
		}
	}
	res := confedit.Result{Versions: map[string]int{}}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, file := range ch.Files {
		if file.Delete {
			_ = f.fs.Remove(file.Path)
			continue
		}
		_ = f.fs.WriteFile(file.Path, file.Content, 0o644)
		v := len(f.versions[file.Path]) + 1
		f.versions[file.Path] = append([]confedit.Version{{
			Path: file.Path, Version: v, CreatedAt: time.Now().UTC(),
			Size: int64(len(file.Content)), Reason: ch.Reason,
		}}, f.versions[file.Path]...)
		f.content[file.Path+"#"+strconv.Itoa(v)] = file.Content
		res.Versions[file.Path] = v
	}
	return res, nil
}

func (f *fakeNginx) History(_ context.Context, p string) ([]confedit.Version, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]confedit.Version(nil), f.versions[p]...), nil
}

func (f *fakeNginx) ReadVersion(_ context.Context, p string, v int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.content[p+"#"+strconv.Itoa(v)]
	if !ok {
		return nil, confedit.ErrNoVersion
	}
	return c, nil
}

func (f *fakeNginx) Restore(ctx context.Context, p string, v int, _ []confedit.Service) (confedit.Result, error) {
	c, err := f.ReadVersion(ctx, p, v)
	if err != nil {
		return confedit.Result{}, err
	}
	return f.Apply(ctx, confedit.Change{Files: []confedit.File{{Path: p, Content: c}}, Reason: fmt.Sprintf("restore version %d", v)})
}

func (f *fakeNginx) Name() string                             { return "nginx" }
func (f *fakeNginx) Validate(context.Context) (string, error) { return "", nil }
func (f *fakeNginx) Reload(context.Context) error             { return nil }
func (f *fakeNginx) Healthy(context.Context) error            { return nil }
func (f *fakeNginx) State(context.Context) (docker.Container, bool, error) {
	return docker.Container{State: docker.ContainerState{Running: true, Status: "running", StartedAt: time.Now().UTC()}}, true, nil
}

func (f *fakeNginx) Status() sites.Status {
	return sites.Status{Done: true, OK: true, At: time.Now().UTC(), Steps: []sites.Step{{Name: "container", Detail: "stand-in for the e2e run", OK: true}}}
}

func fakeNginxDeps(st *store.Store) (*httpapi.NginxDeps, error) {
	paths := nginx.Paths{Root: "/www"}
	mfs := &memFS{files: map[string][]byte{}, dirs: map[string]bool{}}
	fake := &fakeNginx{fs: mfs, versions: map[string][]confedit.Version{}, content: map[string][]byte{}}

	conf, err := paths.RenderMainConf()
	if err != nil {
		return nil, err
	}
	mfs.files[paths.ConfPath()] = conf

	mgr, err := sites.New(sites.Options{Store: st, Applier: fake, Service: fake, Paths: paths, FS: mfs})
	if err != nil {
		return nil, err
	}
	return &httpapi.NginxDeps{
		Sites:   mgr,
		Global:  nginx.NewGlobal(st, fake, fake, paths).ReadWith(mfs.ReadFile),
		History: fake, Service: fake, Reconciler: fake, Paths: paths,
	}, nil
}

// fakeApps stands in for supervisord and the config editor at once, the way
// fakeNginx does for nginx: the HTTP chain and the apps manager are the real
// ones, the process table is a map. It refuses any program file naming
// `bogus_directive`, with a message shaped like supervisord's, so the journey can
// see a rejection; real supervisord is covered by scripts/smoke/5-apps.sh.
type fakeApps struct {
	mu    sync.Mutex
	state map[string]string
	start map[string]int64
}

func newFakeApps() *fakeApps {
	return &fakeApps{state: map[string]string{}, start: map[string]int64{}}
}

func (f *fakeApps) Apply(_ context.Context, ch confedit.Change) (confedit.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, file := range ch.Files {
		if !file.Delete && strings.Contains(string(file.Content), "bogus_directive") {
			return confedit.Result{}, &confedit.ApplyError{
				Stage: confedit.StageValidate, Service: "supervisord",
				Output: "line 2: unknown program option bogus_directive",
				Err:    errors.New("rejected"),
			}
		}
	}
	for _, file := range ch.Files {
		name := strings.TrimSuffix(filepath.Base(file.Path), ".conf")
		switch {
		case file.Delete:
			delete(f.state, name)
		case strings.Contains(string(file.Content), "autostart=true"):
			f.state[name], f.start[name] = "RUNNING", time.Now().Unix()
		default:
			if _, ok := f.state[name]; !ok {
				f.state[name] = "STOPPED"
			}
		}
	}
	return confedit.Result{}, nil
}

func (f *fakeApps) State(context.Context) (string, error) { return "RUNNING", nil }

func (f *fakeApps) Processes(context.Context) ([]apps.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().Unix()
	var out []apps.Process
	for name, state := range f.state {
		p := apps.Process{Name: name, State: state, Now: now}
		if state == "RUNNING" {
			p.PID, p.Start = 4242, f.start[name]
		}
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeApps) ReloadConfig(context.Context) (apps.ConfigDiff, error) {
	return apps.ConfigDiff{}, nil
}
func (f *fakeApps) AddGroup(context.Context, string) error    { return nil }
func (f *fakeApps) RemoveGroup(context.Context, string) error { return nil }

func (f *fakeApps) Start(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[name], f.start[name] = "RUNNING", time.Now().Unix()
	return nil
}

func (f *fakeApps) Stop(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[name] = "STOPPED"
	return nil
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

	nginxDeps, err := fakeNginxDeps(st)
	if err != nil {
		return err
	}

	fa := newFakeApps()
	appsMgr, err := apps.New(apps.Options{
		Store: st, Applier: fa, Supervisor: fa,
		// The same /www paths as fakeNginx: nothing is written to disk, so the root
		// need not exist (and a Windows temp path is not absolute to package path).
		Paths:   apps.Paths{Root: "/www"},
		MakeDir: func(string) error { return nil },
	})
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
		Nginx:    nginxDeps,
		Apps:     appsMgr,
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
