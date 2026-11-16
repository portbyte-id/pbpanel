package docker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func nginxSpec() Spec {
	return Spec{
		Name: "panel-nginx", Image: "panel/nginx:1.29", Role: "nginx",
		Entrypoint: []string{"nginx"},
		Args:       []string{"-c", "/www/panel/nginx/nginx.conf", "-g", "daemon off;"},
		Mounts: []Mount{
			{Path: "/www/panel/nginx", ReadOnly: true},
			{Path: "/www/panel/logs"},
			{Path: "/www/services/sites", ReadOnly: true},
		},
		Labels: map[string]string{"a": "1", "b": "2"},
	}
}

func TestSpecValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Spec)
		root    string
		wantErr string
	}{
		{"valid", func(*Spec) {}, "/www", ""},
		{"no role", func(s *Spec) { s.Role = " " }, "/www", "role is required"},
		{"bad name", func(s *Spec) { s.Name = "panel nginx" }, "/www", "invalid container name"},
		{"bad image", func(s *Spec) { s.Image = "nginx 1.29" }, "/www", "invalid image reference"},
		{"mount over the image's config", func(s *Spec) { s.Mounts = []Mount{{Path: "/etc/nginx"}} }, "/www", "outside /www"},
		{"mount of the root itself", func(s *Spec) { s.Mounts = []Mount{{Path: "/www"}} }, "/www", "outside /www"},
		{"sibling with the root as a prefix", func(s *Spec) { s.Mounts = []Mount{{Path: "/wwwroot/x"}} }, "/www", "outside /www"},
		{"dot-dot escape", func(s *Spec) { s.Mounts = []Mount{{Path: "/www/../etc"}} }, "/www", "clean absolute path"},
		{"trailing slash", func(s *Spec) { s.Mounts = []Mount{{Path: "/www/panel/"}} }, "/www", "clean absolute path"},
		{"relative", func(s *Spec) { s.Mounts = []Mount{{Path: "www/panel"}} }, "/www", "clean absolute path"},
		{"colon would split the bind", func(s *Spec) { s.Mounts = []Mount{{Path: "/www/a:/etc"}} }, "/www", "cannot carry"},
		{"duplicate", func(s *Spec) { s.Mounts = []Mount{{Path: "/www/a"}, {Path: "/www/a", ReadOnly: true}} }, "/www", "listed twice"},
		{"root is /", func(*Spec) {}, "/", "mount root"},
		{"root is relative", func(*Spec) {}, "www", "mount root"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := nginxSpec()
			tc.mutate(&s)
			err := s.Validate(tc.root)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Validate error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestSpecHash(t *testing.T) {
	base := nginxSpec().Hash()
	if len(base) != 64 {
		t.Fatalf("hash %q is not a sha256 hex digest", base)
	}
	// Pinned: a change to how a Spec is hashed recreates every container on
	// every host at the next boot. That must be a decision (bump
	// specHashVersion and this value), never a side effect of a refactor.
	const pinned = "715ee5c0214aea1b1525c0336b2f118cc4e805ed5cc8494605a75511a1856421"
	if base != pinned {
		t.Errorf("hash of the reference spec = %s, want %s", base, pinned)
	}

	same := []struct {
		name   string
		mutate func(*Spec)
	}{
		{"mount order", func(s *Spec) { s.Mounts[0], s.Mounts[2] = s.Mounts[2], s.Mounts[0] }},
		{"label order", func(s *Spec) { s.Labels = map[string]string{"b": "2", "a": "1"} }},
	}
	for _, tc := range same {
		s := nginxSpec()
		tc.mutate(&s)
		if got := s.Hash(); got != base {
			t.Errorf("%s changed the hash", tc.name)
		}
	}
	// Hash must not reorder the caller's slice while canonicalising.
	s := nginxSpec()
	s.Mounts[0], s.Mounts[2] = s.Mounts[2], s.Mounts[0]
	_ = s.Hash()
	if s.Mounts[0].Path != "/www/services/sites" {
		t.Error("Hash sorted the caller's mounts in place")
	}

	different := []struct {
		name   string
		mutate func(*Spec)
	}{
		{"name", func(s *Spec) { s.Name = "panel-nginx2" }},
		{"image", func(s *Spec) { s.Image = "panel/nginx:1.30" }},
		{"role", func(s *Spec) { s.Role = "edge" }},
		{"user", func(s *Spec) { s.User = "www" }},
		{"entrypoint", func(s *Spec) { s.Entrypoint = []string{"/docker-entrypoint.sh"} }},
		{"argument order", func(s *Spec) { s.Args[0], s.Args[2] = s.Args[2], s.Args[0] }},
		{"argument split", func(s *Spec) { s.Args = []string{"-c /www/panel/nginx/nginx.conf", "-g", "daemon off;"} }},
		{"env", func(s *Spec) { s.Env = []string{"TZ=UTC"} }},
		{"mount added", func(s *Spec) { s.Mounts = append(s.Mounts, Mount{Path: "/www/panel/run"}) }},
		{"mount made writable", func(s *Spec) { s.Mounts[0].ReadOnly = false }},
		{"label value", func(s *Spec) { s.Labels = map[string]string{"a": "1", "b": "3"} }},
		{"stop signal", func(s *Spec) { s.StopSignal = "SIGTERM" }},
	}
	for _, tc := range different {
		s := nginxSpec()
		s.Mounts = append([]Mount(nil), s.Mounts...)
		s.Args = append([]string(nil), s.Args...)
		tc.mutate(&s)
		if got := s.Hash(); got == base {
			t.Errorf("changing the %s did not change the hash", tc.name)
		}
	}
}

func TestSpecConfig(t *testing.T) {
	s := nginxSpec()
	s.Labels[LabelManaged] = "false" // a caller cannot disown a panel container
	cfg := s.Config()
	if cfg.NetworkMode != "host" || cfg.RestartPolicy != "unless-stopped" {
		t.Errorf("network %q, restart %q", cfg.NetworkMode, cfg.RestartPolicy)
	}
	want := "/www/panel/nginx:/www/panel/nginx:ro|/www/panel/logs:/www/panel/logs|/www/services/sites:/www/services/sites:ro"
	if got := strings.Join(cfg.Binds, "|"); got != want {
		t.Errorf("binds = %s\nwant    %s", got, want)
	}
	if cfg.Labels[LabelManaged] != "true" || cfg.Labels[LabelRole] != "nginx" || cfg.Labels[LabelSpecHash] != s.Hash() || cfg.Labels["a"] != "1" {
		t.Errorf("labels = %v", cfg.Labels)
	}
}

func TestDecide(t *testing.T) {
	spec := nginxSpec()
	managed := func(mutate func(*Container)) *Container {
		c := &Container{
			ID: "old", ImageID: "sha256:img",
			Labels: map[string]string{LabelManaged: "true", LabelSpecHash: spec.Hash()},
			State:  ContainerState{Status: "running", Running: true},
		}
		if mutate != nil {
			mutate(c)
		}
		return c
	}
	tests := []struct {
		name    string
		current *Container
		imageID string
		want    Action
		wantErr error
	}{
		{"no container", nil, "sha256:img", ActionCreate, nil},
		{"running and current", managed(nil), "sha256:img", ActionNone, nil},
		{"stopped", managed(func(c *Container) { c.State = ContainerState{Status: "exited"} }), "sha256:img", ActionStart, nil},
		{"created, never started", managed(func(c *Container) { c.State = ContainerState{Status: "created"} }), "sha256:img", ActionStart, nil},
		{"spec changed", managed(func(c *Container) { c.Labels[LabelSpecHash] = "stale" }), "sha256:img", ActionRecreate, nil},
		{"image rebuilt under the same tag", managed(nil), "sha256:new", ActionRecreate, nil},
		{"spec changed and stopped: recreate wins", managed(func(c *Container) {
			c.Labels[LabelSpecHash] = "stale"
			c.State = ContainerState{Status: "exited"}
		}), "sha256:img", ActionRecreate, nil},
		{"not ours", &Container{ID: "x", Labels: map[string]string{}, State: ContainerState{Running: true}}, "sha256:img", "", ErrForeignContainer},
		{"ours by hash but not marked managed", &Container{ID: "x", Labels: map[string]string{LabelSpecHash: spec.Hash()}}, "sha256:img", "", ErrForeignContainer},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason, err := Decide(spec, tc.imageID, tc.current)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("action = %q (%s), want %q", got, reason, tc.want)
			}
			if err == nil && reason == "" {
				t.Error("a decision came without a reason")
			}
		})
	}
}

// fakeUnitEngine is an in-memory Engine that records every mutating call.
type fakeUnitEngine struct {
	image     *Image
	container *Container
	calls     []string
	failOn    string
}

func (f *fakeUnitEngine) record(call string) error {
	f.calls = append(f.calls, call)
	if f.failOn != "" && strings.HasPrefix(call, f.failOn) {
		return errors.New("injected failure on " + f.failOn)
	}
	return nil
}

func (f *fakeUnitEngine) ImageInspect(context.Context, string) (Image, error) {
	if f.image == nil {
		return Image{}, &APIError{Op: "inspect image", Status: 404, Message: "No such image"}
	}
	return *f.image, nil
}

func (f *fakeUnitEngine) ContainerInspect(context.Context, string) (Container, error) {
	if f.container == nil {
		return Container{}, &APIError{Op: "inspect container", Status: 404, Message: "No such container"}
	}
	return *f.container, nil
}

func (f *fakeUnitEngine) ContainerCreate(_ context.Context, name string, cfg ContainerConfig) (string, error) {
	if err := f.record("create " + name); err != nil {
		return "", err
	}
	f.container = &Container{ID: "new", Name: name, ImageID: f.image.ID, Labels: cfg.Labels, State: ContainerState{Status: "created"}}
	return "new", nil
}

func (f *fakeUnitEngine) ContainerStart(_ context.Context, id string) error {
	if err := f.record("start " + id); err != nil {
		return err
	}
	f.container.State = ContainerState{Status: "running", Running: true}
	return nil
}

func (f *fakeUnitEngine) ContainerStop(_ context.Context, id string, _ time.Duration) error {
	return f.record("stop " + id)
}

func (f *fakeUnitEngine) ContainerRemove(_ context.Context, id string, _ bool) error {
	if err := f.record("remove " + id); err != nil {
		return err
	}
	f.container = nil
	return nil
}

func TestEnsure(t *testing.T) {
	spec := nginxSpec()
	img := &Image{ID: "sha256:img"}
	existing := func(mutate func(*Container)) *Container {
		c := &Container{
			ID: "old", Name: spec.Name, ImageID: img.ID,
			Labels: map[string]string{LabelManaged: "true", LabelSpecHash: spec.Hash()},
			State:  ContainerState{Status: "running", Running: true},
		}
		if mutate != nil {
			mutate(c)
		}
		return c
	}
	tests := []struct {
		name      string
		container *Container
		wantCalls string
		wantID    string
		want      Action
	}{
		{"missing: create and start", nil, "create panel-nginx|start new", "new", ActionCreate},
		{"current: nothing", existing(nil), "", "old", ActionNone},
		{"stopped: start", existing(func(c *Container) { c.State = ContainerState{Status: "exited"} }), "start old", "old", ActionStart},
		{"stale spec: stop, remove, create, start", existing(func(c *Container) { c.Labels[LabelSpecHash] = "stale" }),
			"stop old|remove old|create panel-nginx|start new", "new", ActionRecreate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eng := &fakeUnitEngine{image: img, container: tc.container}
			res, err := Ensure(context.Background(), eng, spec, "/www")
			if err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			if got := strings.Join(eng.calls, "|"); got != tc.wantCalls {
				t.Errorf("calls = %q, want %q", got, tc.wantCalls)
			}
			if res.Action != tc.want || res.ID != tc.wantID {
				t.Errorf("result = %+v, want action %q id %q", res, tc.want, tc.wantID)
			}

			// Idempotency: whatever the first call did, a second one does nothing.
			eng.calls = nil
			again, err := Ensure(context.Background(), eng, spec, "/www")
			if err != nil || again.Action != ActionNone || len(eng.calls) != 0 {
				t.Errorf("second Ensure = %+v, %v, calls %q; want no action", again, err, eng.calls)
			}
		})
	}
}

func TestEnsureRefusals(t *testing.T) {
	spec := nginxSpec()
	ctx := context.Background()

	t.Run("image absent: nothing is touched", func(t *testing.T) {
		eng := &fakeUnitEngine{}
		if _, err := Ensure(ctx, eng, spec, "/www"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("error = %v, want ErrNotFound", err)
		}
		if len(eng.calls) != 0 {
			t.Errorf("calls = %q", eng.calls)
		}
	})
	t.Run("a foreign container is never removed", func(t *testing.T) {
		eng := &fakeUnitEngine{image: &Image{ID: "i"}, container: &Container{ID: "theirs", Labels: map[string]string{}}}
		if _, err := Ensure(ctx, eng, spec, "/www"); !errors.Is(err, ErrForeignContainer) {
			t.Fatalf("error = %v, want ErrForeignContainer", err)
		}
		if len(eng.calls) != 0 {
			t.Errorf("calls = %q", eng.calls)
		}
	})
	t.Run("an invalid spec reaches no engine call", func(t *testing.T) {
		bad := spec
		bad.Mounts = []Mount{{Path: "/etc/nginx"}}
		eng := &fakeUnitEngine{image: &Image{ID: "i"}}
		if _, err := Ensure(ctx, eng, bad, "/www"); err == nil || len(eng.calls) != 0 {
			t.Fatalf("error = %v, calls = %q", err, eng.calls)
		}
	})
	t.Run("a failed remove does not create a second container", func(t *testing.T) {
		eng := &fakeUnitEngine{image: &Image{ID: "i"}, failOn: "remove", container: &Container{
			ID: "old", Labels: map[string]string{LabelManaged: "true", LabelSpecHash: "stale"}, State: ContainerState{Running: true},
		}}
		if _, err := Ensure(ctx, eng, spec, "/www"); err == nil {
			t.Fatal("Ensure succeeded although remove failed")
		}
		if got := strings.Join(eng.calls, "|"); got != "stop old|remove old" {
			t.Errorf("calls = %q", got)
		}
	})
}
