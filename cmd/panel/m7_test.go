package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/cron"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/logs"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/sites"
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/sysinfo"
)

type fakeSiteList []sites.Site

func (f fakeSiteList) List(context.Context) ([]sites.Site, error) { return f, nil }

func TestSiteLogSources(t *testing.T) {
	ver := "8.4"
	list := fakeSiteList{
		{Domain: "a.test"},
		{Domain: "b.test", PHPVersion: &ver},
	}
	got, err := siteLogSources(context.Background(), list, nginx.Paths{Root: "/www"}, php.Paths{Root: "/www"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"site:a.test:access": "/www/panel/logs/sites/a.test.access.log",
		"site:a.test:error":  "/www/panel/logs/sites/a.test.error.log",
		"site:b.test:access": "/www/panel/logs/sites/b.test.access.log",
		"site:b.test:error":  "/www/panel/logs/sites/b.test.error.log",
		"site:b.test:php":    "/www/panel/logs/sites/b.test.php.log",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sources, want %d: %+v", len(got), len(want), got)
	}
	reg := logs.NewRegistry(nil)
	reg.AddProvider(logs.ProviderFunc(func(context.Context) ([]logs.Source, error) { return got, nil }))
	listed, _ := reg.List(context.Background())
	if len(listed) != len(want) {
		t.Fatalf("the registry rejected a source: %d of %d listed", len(listed), len(want))
	}
	for _, s := range got {
		if want[s.ID] != s.Path || s.Kind != logs.KindSite {
			t.Errorf("%s: path %q kind %q", s.ID, s.Path, s.Kind)
		}
	}
}

func TestNewestAppLogFollowsTheTimestampedFile(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.ToSlash(filepath.Join(dir, "out.log"))
	if got := newestAppLog(plain); got != plain {
		t.Fatalf("with no file the plain name is returned, got %q", got)
	}
	old := filepath.Join(dir, "out.log.20261101")
	cur := filepath.Join(dir, "out.log.20261117")
	for _, p := range []string{old, cur, filepath.Join(dir, "other.txt")} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if got := newestAppLog(plain); filepath.Base(got) != "out.log.20261117" {
		t.Fatalf("got %q", got)
	}
}

type fakeCronRuns struct {
	jobs []cron.Job
	runs map[int64][]cron.Run
}

func (f fakeCronRuns) List(context.Context) ([]cron.Job, error) { return f.jobs, nil }
func (f fakeCronRuns) Runs(_ context.Context, id int64, _ int) ([]cron.Run, error) {
	return f.runs[id], nil
}

func TestCronLogSourcesStayUnderTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cron")
	in := filepath.Join(root, "backup", "7.log")
	f := fakeCronRuns{
		jobs: []cron.Job{{ID: 1, Name: "backup"}, {ID: 2, Name: "evil"}, {ID: 3, Name: "never"}, {ID: 4, Name: "nolog"}},
		runs: map[int64][]cron.Run{
			1: {{ID: 7, JobID: 1, LogPath: in}},
			2: {{ID: 8, JobID: 2, LogPath: filepath.Join(root, "..", "secret.log")}},
			4: {{ID: 9, JobID: 4}},
		},
	}
	got, err := cronLogSources(context.Background(), f, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "cron:backup" || got[0].Path != in || got[0].Kind != logs.KindCron {
		t.Fatalf("got %+v", got)
	}
}

type fakeLogReader struct {
	inspectErr error
	payload    string
	logErr     error
	gotOpts    docker.LogOptions
	ctxDone    chan struct{}
	block      bool
}

func (f *fakeLogReader) ContainerInspect(context.Context, string) (docker.Container, error) {
	return docker.Container{}, f.inspectErr
}
func (f *fakeLogReader) ContainerLogs(ctx context.Context, _ string, o docker.LogOptions, stdout, stderr io.Writer) error {
	f.gotOpts = o
	// stdout and stderr land in the same stream, in order.
	for i, part := range strings.SplitAfter(f.payload, "\n") {
		if part == "" {
			continue
		}
		w := stdout
		if i%2 == 1 {
			w = stderr
		}
		if _, err := io.WriteString(w, part); err != nil {
			return err
		}
	}
	if f.block {
		<-ctx.Done()
		close(f.ctxDone)
		return ctx.Err()
	}
	return f.logErr
}

func TestDockerLogsAdapterInterleavesBothStreams(t *testing.T) {
	f := &fakeLogReader{payload: "out one\nerr two\nout three\n"}
	rc, err := dockerLogs{f}.ContainerLogs(context.Background(), "panel-nginx", logs.DockerOptions{Tail: 30, Follow: false})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != f.payload {
		t.Fatalf("got %q", got)
	}
	if f.gotOpts.Tail != 30 || f.gotOpts.Follow {
		t.Fatalf("options %+v", f.gotOpts)
	}
}

func TestDockerLogsAdapterReportsAMissingContainerUpFront(t *testing.T) {
	f := &fakeLogReader{inspectErr: docker.ErrNotFound}
	if _, err := (dockerLogs{f}).ContainerLogs(context.Background(), "gone", logs.DockerOptions{}); !errors.Is(err, docker.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestDockerLogsAdapterCloseEndsTheCopy(t *testing.T) {
	f := &fakeLogReader{payload: "line\n", block: true, ctxDone: make(chan struct{})}
	rc, err := dockerLogs{f}.ContainerLogs(context.Background(), "panel-nginx", logs.DockerOptions{Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(rc, buf); err != nil || string(buf) != "line\n" {
		t.Fatalf("first read %q %v", buf, err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.ctxDone:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the reader did not cancel the Docker request")
	}
}

type fakeSignaler struct {
	mu     sync.Mutex
	states map[string]docker.Container
	errs   map[string]error
	killed []string
}

func (f *fakeSignaler) ContainerInspect(_ context.Context, id string) (docker.Container, error) {
	if err := f.errs[id]; err != nil {
		return docker.Container{}, err
	}
	c, ok := f.states[id]
	if !ok {
		return docker.Container{}, docker.ErrNotFound
	}
	return c, nil
}
func (f *fakeSignaler) ContainerKill(_ context.Context, id, sig string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, id+" "+sig)
	return nil
}

func TestReopenContainerLogsSignalsOnlyRunningContainers(t *testing.T) {
	f := &fakeSignaler{states: map[string]docker.Container{
		"panel-nginx":   {State: docker.ContainerState{Running: true}},
		"panel-php-8.3": {State: docker.ContainerState{Running: false}},
	}}
	hook := reopenContainerLogs(f, func(context.Context) []string {
		return []string{"panel-nginx", "panel-php-8.3", "panel-php-8.4"}
	})
	if err := hook(context.Background()); err != nil {
		t.Fatalf("a stopped or missing container is not an error: %v", err)
	}
	if len(f.killed) != 1 || f.killed[0] != "panel-nginx USR1" {
		t.Fatalf("signalled %v", f.killed)
	}

	f.errs = map[string]error{"panel-php-8.4": errors.New("socket closed")}
	if err := hook(context.Background()); err == nil || !strings.Contains(err.Error(), "panel-php-8.4") {
		t.Fatalf("a failing inspect must be reported: %v", err)
	}
}

// The panel runs no containers: nginx and PHP are systemd units. The versions
// setting names the units the Services page shows, so it is followed on every
// call.
func TestPanelUnitsFollowTheVersionSetting(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg, err := settings.New(st)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := reg.Set(ctx, settings.KeyPHPVersions, "8.3,8.4"); err != nil {
		t.Fatal(err)
	}
	if got := panelContainers(ctx, reg); len(got) != 0 {
		t.Fatalf("panel containers = %v, want none", got)
	}
	want := []string{"panel-php-8.3.service", "panel-php-8.4.service"}
	if got := panelUnits(ctx, reg); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("units = %v, want %v", got, want)
	}
	if err := reg.Set(ctx, settings.KeyPHPVersions, "8.5"); err != nil {
		t.Fatal(err)
	}
	if got := panelUnits(ctx, reg); len(got) != 1 || got[0] != "panel-php-8.5.service" {
		t.Fatalf("units after the setting changed = %v", got)
	}
}

func TestSysinfoFacadeSatisfiesTheServiceInterface(t *testing.T) {
	// The compile-time check is the point: the sampler and the service list must
	// stay disjoint for the embedded struct to have one method set.
	var _ interface {
		Interval() time.Duration
		History() []sysinfo.Snapshot
		Latest() (sysinfo.Snapshot, bool)
		Subscribe() (<-chan sysinfo.Snapshot, func())
		List(context.Context) []sysinfo.Service
		Control(context.Context, string, sysinfo.Action) error
	} = sysinfoFacade{}
}
