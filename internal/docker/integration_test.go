//go:build linux && integration

package docker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Runs against a real Docker Engine:
//
//	go test -tags integration ./internal/docker/
//
// It needs the socket and one small local image with `sleep`, `cat` and `ls`
// (PANEL_TEST_IMAGE, default alpine:latest). It pulls nothing: a missing image
// skips the test rather than depending on a registry.
func TestEngineIntegration(t *testing.T) {
	base := os.Getenv("PANEL_TEST_IMAGE")
	if base == "" {
		base = "alpine:latest"
	}
	c, err := New(DefaultSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	info, err := c.Ping(ctx)
	if err != nil {
		t.Skipf("no usable Docker Engine: %v", err)
	}
	t.Logf("Engine API %s", info.APIVersion)
	if _, err := c.ImageInspect(ctx, base); errors.Is(err, ErrNotFound) {
		t.Skipf("image %s is not present locally", base)
	} else if err != nil {
		t.Fatal(err)
	}

	const (
		tag  = "panel/integration-test:1"
		name = "panel-integration-test"
	)
	root := t.TempDir()
	_ = c.ContainerRemove(ctx, name, true)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()
		_ = c.ContainerRemove(cctx, name, true)
		_ = c.call(cctx, request{op: "remove image", method: "DELETE", path: "/images/" + tag}, nil)
	})

	// Build: the derived-image path the nginx container uses (step 1.1).
	var lines []string
	img, err := c.ImageBuild(ctx, BuildOptions{
		Tag:        tag,
		Dockerfile: []byte("FROM " + base + "\nRUN echo built > /built.txt\n"),
		Labels:     map[string]string{LabelManaged: "true"},
	}, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatalf("ImageBuild: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if img.ID == "" || len(lines) == 0 {
		t.Fatalf("build returned image %+v with %d progress lines", img, len(lines))
	}

	// A failing build must fail, although the Engine answers 200.
	_, err = c.ImageBuild(ctx, BuildOptions{Tag: "panel/integration-test:broken", Dockerfile: []byte("FROM " + base + "\nRUN exit 7\n")}, nil)
	if err == nil {
		t.Error("a build whose RUN step exits 7 reported success")
	} else {
		t.Logf("failed build reported as: %v", err)
	}

	spec := Spec{
		Name: name, Image: tag, Role: "integration-test",
		Entrypoint: []string{"sleep"}, Args: []string{"300"},
		Mounts:     []Mount{{Path: root, ReadOnly: true}},
		StopSignal: "SIGKILL",
	}
	// The mount root is the parent of the temp dir here; in production it is /www.
	mountRoot := root[:strings.LastIndex(root, "/")]

	res, err := Ensure(ctx, c, spec, mountRoot)
	if err != nil || res.Action != ActionCreate {
		t.Fatalf("first Ensure = %+v, %v", res, err)
	}
	if again, err := Ensure(ctx, c, spec, mountRoot); err != nil || again.Action != ActionNone || again.ID != res.ID {
		t.Fatalf("second Ensure = %+v, %v; want no action on the same container", again, err)
	}

	got, err := c.ContainerInspect(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if !got.State.Running || got.ImageID != img.ID || got.Labels[LabelSpecHash] != spec.Hash() || got.State.StartedAt.IsZero() {
		t.Errorf("inspect = %+v", got)
	}

	// Exec: stdout, stderr and the exit code each arrive where they belong.
	out, err := c.Exec(ctx, name, ExecOptions{Cmd: []string{"cat", "/built.txt"}})
	if err != nil || out.ExitCode != 0 || string(out.Stdout) != "built\n" || len(out.Stderr) != 0 {
		t.Errorf("exec cat = %+v, %v", out, err)
	}
	out, err = c.Exec(ctx, name, ExecOptions{Cmd: []string{"ls", "/definitely-not-here"}})
	if err != nil || out.ExitCode == 0 || len(out.Stdout) != 0 || !bytes.Contains(out.Stderr, []byte("definitely-not-here")) {
		t.Errorf("exec ls of a missing path = %+v, %v", out, err)
	}

	// Stopped by hand, Ensure starts the same container again.
	if err := c.ContainerStop(ctx, name, 0); err != nil {
		t.Fatal(err)
	}
	if res, err := Ensure(ctx, c, spec, mountRoot); err != nil || res.Action != ActionStart {
		t.Fatalf("Ensure after a stop = %+v, %v", res, err)
	}

	// A changed spec replaces the container.
	spec.Args = []string{"600"}
	replaced, err := Ensure(ctx, c, spec, mountRoot)
	if err != nil || replaced.Action != ActionRecreate || replaced.ID == res.ID {
		t.Fatalf("Ensure after a spec change = %+v, %v", replaced, err)
	}

	// Removed by hand (the step 1.9 scenario), Ensure creates it again.
	if err := c.ContainerRemove(ctx, name, true); err != nil {
		t.Fatal(err)
	}
	if res, err := Ensure(ctx, c, spec, mountRoot); err != nil || res.Action != ActionCreate {
		t.Fatalf("Ensure after a removal = %+v, %v", res, err)
	}

	// A signal reaches PID 1: SIGKILL ends `sleep`.
	if err := c.ContainerKill(ctx, name, "KILL"); err != nil {
		t.Fatal(err)
	}
	if code, err := c.ContainerWait(ctx, name); err != nil || code != 137 {
		t.Errorf("exit after SIGKILL = %d, %v; want 137", code, err)
	}

	// One-shot run, as a validator does when the service container is down.
	once, err := c.RunOnce(ctx, name+"-once", ContainerConfig{Image: tag, Entrypoint: []string{"ls"}, Cmd: []string{"/built.txt", "/nope"}, NetworkMode: "host"})
	if err != nil || once.ExitCode == 0 || !strings.Contains(string(once.Stdout), "/built.txt") || !strings.Contains(string(once.Stderr), "/nope") {
		t.Errorf("RunOnce = %+v, %v", once, err)
	}
	if _, err := c.ContainerInspect(ctx, name+"-once"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the one-shot container was left behind: %v", err)
	}
}
