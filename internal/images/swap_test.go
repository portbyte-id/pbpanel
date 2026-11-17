package images

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/docker"
)

// fakeDocker is the container side of the Engine: enough of it for
// docker.Ensure and the stop and remove of a swap.
type fakeDocker struct {
	mu         sync.Mutex
	images     map[string]bool
	containers map[string]*docker.Container
	calls      []string
}

func newFakeDocker(images ...string) *fakeDocker {
	f := &fakeDocker{images: map[string]bool{}, containers: map[string]*docker.Container{}}
	for _, img := range images {
		f.images[img] = true
	}
	return f
}

func (f *fakeDocker) log(s string) { f.calls = append(f.calls, s) }

func (f *fakeDocker) ImageInspect(_ context.Context, ref string) (docker.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.images[ref] {
		return docker.Image{}, docker.ErrNotFound
	}
	return docker.Image{ID: "sha256:" + ref}, nil
}

func (f *fakeDocker) ContainerInspect(_ context.Context, name string) (docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[name]
	if !ok {
		return docker.Container{}, docker.ErrNotFound
	}
	return *c, nil
}

func (f *fakeDocker) ContainerCreate(_ context.Context, name string, cfg docker.ContainerConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("create " + cfg.Image)
	f.containers[name] = &docker.Container{ID: name, Name: name, ImageID: "sha256:" + cfg.Image, Labels: cfg.Labels}
	return name, nil
}

func (f *fakeDocker) ContainerStart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("start " + id)
	for _, c := range f.containers {
		if c.ID == id {
			c.State = docker.ContainerState{Running: true, Status: "running", StartedAt: time.Unix(1, 0)}
		}
	}
	return nil
}

func (f *fakeDocker) ContainerStop(_ context.Context, name string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("stop " + name)
	c, ok := f.containers[name]
	if !ok {
		return docker.ErrNotFound
	}
	c.State = docker.ContainerState{Status: "exited"}
	return nil
}

func (f *fakeDocker) ContainerRemove(_ context.Context, name string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("remove " + name)
	if _, ok := f.containers[name]; !ok {
		return docker.ErrNotFound
	}
	delete(f.containers, name)
	return nil
}

func (f *fakeDocker) imageOf(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[name]
	if !ok {
		return ""
	}
	return strings.TrimPrefix(c.ImageID, "sha256:")
}

func (f *fakeDocker) ran(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func plan(f *fakeDocker, badImage string) SwapPlan {
	return SwapPlan{
		Name:  "panel-php-8.3",
		Mount: "/www",
		Spec: func(image string) docker.Spec {
			return docker.Spec{Name: "panel-php-8.3", Image: image, Role: "php-8.3", Mounts: []docker.Mount{{Path: "/www/panel/run"}}}
		},
		Healthy: func(context.Context) error {
			if f.imageOf("panel-php-8.3") == badImage {
				return errors.New("the master exited right after the start")
			}
			return nil
		},
	}
}

func TestSwapReplacesTheContainer(t *testing.T) {
	f := newFakeDocker("panel/php:8.3-old", "panel/php:8.3-new")
	f.containers["panel-php-8.3"] = &docker.Container{
		ID: "panel-php-8.3", Name: "panel-php-8.3", ImageID: "sha256:panel/php:8.3-old",
		Labels: map[string]string{docker.LabelManaged: "true"},
		State:  docker.ContainerState{Running: true, Status: "running"},
	}
	if err := Swap(context.Background(), f, plan(f, ""), "panel/php:8.3-old", "panel/php:8.3-new"); err != nil {
		t.Fatalf("Swap = %v", err)
	}
	if got := f.imageOf("panel-php-8.3"); got != "panel/php:8.3-new" {
		t.Errorf("the container runs %q, want the new image", got)
	}
	if !f.ran("stop panel-php-8.3") || !f.ran("remove panel-php-8.3") {
		t.Errorf("the old container was not retired: %v", f.calls)
	}
}

func TestSwapRestoresThePreviousImageWhenTheNewOneFails(t *testing.T) {
	f := newFakeDocker("panel/php:8.3-old", "panel/php:8.3-new")
	f.containers["panel-php-8.3"] = &docker.Container{
		ID: "panel-php-8.3", Name: "panel-php-8.3", ImageID: "sha256:panel/php:8.3-old",
		Labels: map[string]string{docker.LabelManaged: "true"},
		State:  docker.ContainerState{Running: true, Status: "running"},
	}
	err := Swap(context.Background(), f, plan(f, "panel/php:8.3-new"), "panel/php:8.3-old", "panel/php:8.3-new")
	if err == nil {
		t.Fatal("a new container that failed its check was accepted")
	}
	if !strings.Contains(err.Error(), "running again") {
		t.Errorf("err = %v, want the restore reported", err)
	}
	if got := f.imageOf("panel-php-8.3"); got != "panel/php:8.3-old" {
		t.Errorf("the container runs %q after the failed swap, want the old image", got)
	}
}

func TestSwapReportsWhenNothingCanBeRestored(t *testing.T) {
	// The previous image is gone too: the swap must say so, not claim success.
	f := newFakeDocker("panel/php:8.3-new")
	err := Swap(context.Background(), f, plan(f, "panel/php:8.3-new"), "panel/php:8.3-gone", "panel/php:8.3-new")
	if err == nil || !strings.Contains(err.Error(), "not restored") {
		t.Errorf("err = %v, want a report that the previous image is not restored", err)
	}
}
