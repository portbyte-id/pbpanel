package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/docker"
)

type fakeRetirer struct {
	c          *docker.Container // nil: no such container
	inspectErr error
	calls      []string
}

func (f *fakeRetirer) ContainerInspect(context.Context, string) (docker.Container, error) {
	if f.inspectErr != nil {
		return docker.Container{}, f.inspectErr
	}
	if f.c == nil {
		return docker.Container{}, docker.ErrNotFound
	}
	return *f.c, nil
}
func (f *fakeRetirer) ContainerStop(_ context.Context, id string, _ time.Duration) error {
	f.calls = append(f.calls, "stop "+id)
	return nil
}
func (f *fakeRetirer) ContainerRemove(_ context.Context, id string, _ bool) error {
	f.calls = append(f.calls, "remove "+id)
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRetireRemovesThePanelsOwnOldContainer(t *testing.T) {
	f := &fakeRetirer{c: &docker.Container{Labels: map[string]string{docker.LabelManaged: "true"}}}
	if err := retireNginxContainer(context.Background(), f, quiet()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "stop panel-nginx,remove panel-nginx" {
		t.Errorf("calls = %v", f.calls)
	}
}

func TestRetireLeavesAForeignContainerAndSaysSo(t *testing.T) {
	f := &fakeRetirer{c: &docker.Container{Labels: map[string]string{}}}
	err := retireNginxContainer(context.Background(), f, quiet())
	if err == nil || !strings.Contains(err.Error(), "did not make it") {
		t.Errorf("err = %v, want a refusal to touch a container the panel did not make", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("a foreign container was touched: %v", f.calls)
	}
}

func TestRetireIsANoOpOnAFreshHostOrWithoutDocker(t *testing.T) {
	for name, f := range map[string]*fakeRetirer{
		"no container": {},
		"docker down":  {inspectErr: errors.New("dial unix /var/run/docker.sock: connect: no such file or directory")},
	} {
		if err := retireNginxContainer(context.Background(), f, quiet()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if len(f.calls) != 0 {
			t.Errorf("%s: calls = %v", name, f.calls)
		}
	}
}
