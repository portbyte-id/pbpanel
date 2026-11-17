//go:build integration && linux

package php

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/store"
)

// noApply is the applier of a test that writes no configuration through confedit.
type noApply struct{}

func (noApply) Apply(context.Context, confedit.Change) (confedit.Result, error) {
	return confedit.Result{}, errors.New("this test does not apply configuration")
}

// TestRealImageLifecycle runs the M8 PHP lifecycle on a real Engine: a build with
// extensions is checked and swapped under a running container; a build that
// fails leaves that container alone; an activation puts the earlier build back.
// It needs Docker, the `php:8.3-fpm` image (pulled on demand) and network access
// for the PECL download. Run with `-tags integration`.
func TestRealImageLifecycle(t *testing.T) {
	const ver = "8.3"
	ctx := context.Background()
	root := t.TempDir()
	p := Paths{Root: root}

	eng, err := docker.New(docker.DefaultSocket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)

	st, err := store.Open(filepath.Join(root, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	m, err := New(Options{
		Store: st, Engine: eng, Applier: noApply{}, Paths: p,
		Supported: func(context.Context) ([]string, error) { return []string{ver}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Remove(context.Background(), ver, nil) })

	// The files the containers read, written as the confedit path would write
	// them: php.ini copied from the image, then the rendered FPM and form files.
	for _, dir := range []string{p.Dir(ver), p.ConfDir(ver), p.PoolDir(ver), p.RunDir(), p.SiteLogDir(), p.SitesRoot()} {
		if err := os.MkdirAll(filepath.FromSlash(dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := eng.ImagePull(ctx, BaseImage(ver), nil); err != nil {
		t.Fatalf("pull %s: %v", BaseImage(ver), err)
	}
	ini, err := eng.RunOnce(ctx, "m8-it-ini", docker.ContainerConfig{
		Image: BaseImage(ver), Entrypoint: []string{"cat"}, Cmd: []string{"/usr/local/etc/php/php.ini-production"},
	})
	if err != nil || ini.ExitCode != 0 {
		t.Fatalf("read php.ini-production: %v exit %d", err, ini.ExitCode)
	}
	writeFile(t, p.IniPath(ver), ini.Stdout)
	fpm, err := p.RenderFPMConf(ver)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p.FPMConf(ver), fpm)
	form, err := RenderFormIni(DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p.FormIni(ver), form)

	// Install with no extensions: build A serves; nothing runs yet.
	if err := m.StartPull(ctx, ver); err != nil {
		t.Fatal(err)
	}
	if err := m.Pull(ctx, ver, nil, nil); err != nil {
		t.Fatalf("install = %v", err)
	}
	a, err := m.Get(ctx, ver)
	if err != nil || a.Current == nil {
		t.Fatalf("after install: %+v, %v", a, err)
	}

	// The first site would start the container on build A.
	if _, err := m.EnsureContainer(ctx, ver); err != nil {
		t.Fatalf("start on build A: %v", err)
	}
	before, found, err := m.ContainerState(ctx, ver)
	if err != nil || !found || !before.State.Running {
		t.Fatalf("container after start: found=%v running=%v err=%v", found, before.State.Running, err)
	}
	if mods := phpModules(t, eng, ver); strings.Contains(mods, "\nintl\n") {
		t.Fatal("build A already has intl")
	}

	// A build with intl and redis is checked, then swapped under the running container.
	exts, err := ParseExtensions([]string{"intl", "redis"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Rebuild(ctx, ver, exts, nil)
	if err != nil {
		t.Fatalf("build B = %v", err)
	}
	after, found, err := m.ContainerState(ctx, ver)
	if err != nil || !found || !after.State.Running {
		t.Fatalf("container after the swap: found=%v running=%v err=%v", found, after.State.Running, err)
	}
	if after.ID == before.ID {
		t.Error("the swap kept the old container; a new one must replace it")
	}
	if mods := phpModules(t, eng, ver); !strings.Contains(mods, "intl") || !strings.Contains(mods, "redis") {
		t.Errorf("the swapped container's modules lack intl or redis:\n%s", mods)
	}
	if cur, _ := m.Get(ctx, ver); cur.Current == nil || cur.Current.ID != b.ID {
		t.Errorf("current after the swap = %+v, want build %s", cur.Current, b.ID)
	}

	// A build that cannot be made fails; the container that serves is untouched.
	bogus, err := ParseExtensions([]string{"m8nosuchpeclpackage"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rebuild(ctx, ver, bogus, nil); err == nil {
		t.Fatal("a build of a package that does not exist succeeded")
	}
	still, found, err := m.ContainerState(ctx, ver)
	if err != nil || !found || !still.State.Running || still.ID != after.ID {
		t.Errorf("the serving container changed after a failed build: id %s -> %s running=%v", after.ID, still.ID, still.State.Running)
	}
	if cur, _ := m.Get(ctx, ver); cur.Current == nil || cur.Current.ID != b.ID {
		t.Errorf("current moved after a failed build: %+v", cur.Current)
	}

	// Activation puts build A back, and the container runs its image again.
	if _, err := m.Activate(ctx, ver, a.Current.ID); err != nil {
		t.Fatalf("activate A = %v", err)
	}
	back, found, err := m.ContainerState(ctx, ver)
	if err != nil || !found || !back.State.Running {
		t.Fatalf("container after activating A: found=%v running=%v err=%v", found, back.State.Running, err)
	}
	if back.ImageID != a.Current.ImageID {
		t.Errorf("container image after activating A = %s, want %s", back.ImageID, a.Current.ImageID)
	}
	if mods := phpModules(t, eng, ver); strings.Contains(mods, "\nintl\n") {
		t.Error("build A is serving but still lists intl")
	}
}

// phpModules runs `php -m` in the running container of a version.
func phpModules(t *testing.T, eng *docker.Client, ver string) string {
	t.Helper()
	res, err := eng.Exec(context.Background(), ContainerName(ver), docker.ExecOptions{Cmd: []string{"php", "-m"}})
	if err != nil {
		t.Fatalf("php -m: %v", err)
	}
	return "\n" + strings.ReplaceAll(res.Output(), "\r", "") + "\n"
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.FromSlash(path), content, 0o644); err != nil {
		t.Fatal(err)
	}
}
