//go:build linux && integration

package docker

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// Runs the Docker page's read side and controls against a real Engine:
//
//	go test -tags integration -run TestManageIntegration ./internal/docker/
//
// The unit tests prove the code against Engine answers written by hand; this is
// the check that those answers are what Docker really sends.
func TestManageIntegration(t *testing.T) {
	base := os.Getenv("PANEL_TEST_IMAGE")
	if base == "" {
		base = "alpine:latest"
	}
	c, err := New(DefaultSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := c.Ping(ctx); err != nil {
		t.Skipf("no usable Docker Engine: %v", err)
	}
	if _, err := c.ImageInspect(ctx, base); errors.Is(err, ErrNotFound) {
		t.Skipf("image %s is not present locally", base)
	} else if err != nil {
		t.Fatal(err)
	}

	const name = "panel-manage-integration"
	_ = c.ContainerRemove(ctx, name, true)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()
		_ = c.ContainerRemove(cctx, name, true)
	})

	id, err := c.ContainerCreate(ctx, name, ContainerConfig{
		Image: base, Cmd: []string{"sleep", "300"},
		Labels: map[string]string{LabelManaged: "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}

	info, err := c.Info(ctx)
	if err != nil || info.ServerVersion == "" || info.CPUs < 1 || info.Running < 1 {
		t.Fatalf("Info: %v %+v", err, info)
	}

	var mine ContainerSummary
	list, err := c.ContainerList(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == id {
			mine = s
		}
	}
	if mine.Name != name || mine.State != "running" || !mine.Managed || mine.ImageID == "" || mine.Created.IsZero() {
		t.Fatalf("listed container: %+v", mine)
	}

	st, err := c.ContainerStats(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.Name != name || st.MemUsage <= 0 || st.MemLimit <= 0 || st.Pids < 1 || st.CPUPercent < 0 {
		t.Fatalf("stats: %+v", st)
	}
	t.Logf("stats: cpu=%.2f%% mem=%d/%d pids=%d", st.CPUPercent, st.MemUsage, st.MemLimit, st.Pids)
	if all := c.StatsAll(ctx, []string{id}); len(all) != 1 {
		t.Fatalf("StatsAll: %+v", all)
	}

	top, err := c.ContainerTop(ctx, id)
	if err != nil || len(top.Titles) == 0 || len(top.Processes) == 0 {
		t.Fatalf("top: %v %+v", err, top)
	}

	if err := c.ContainerRestart(ctx, id, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if got, err := c.ContainerInspect(ctx, id); err != nil || !got.State.Running {
		t.Fatalf("after restart: %v %+v", err, got.State)
	}

	imgs, err := c.ImageList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, im := range imgs {
		if im.ID == mine.ImageID {
			found = true
			if im.Size <= 0 || im.Created.IsZero() {
				t.Errorf("image row: %+v", im)
			}
		}
	}
	if !found {
		t.Errorf("the container's image %s is not in the image list", mine.ImageID)
	}

	if _, err := c.VolumeList(ctx); err != nil {
		t.Fatalf("VolumeList: %v", err)
	}
	if err := c.VolumeRemove(ctx, "panel-no-such-volume"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing a missing volume = %v, want ErrNotFound", err)
	}

	if err := c.ContainerStop(ctx, id, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContainerStats(ctx, id); err != nil {
		// A stopped container still answers stats with zeroes on current Engines;
		// either outcome is fine, it just must not panic or hang.
		t.Logf("stats of a stopped container: %v", err)
	}
}
