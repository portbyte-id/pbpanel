package nginx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
)

// Names and images. The image is the official one plus the `www` account at the
// fixed UID (decision 1.1), so workers own and read files under /www.
const (
	ContainerName = "panel-nginx"
	BaseImage     = "nginx:1.29"
	Image         = "panel/nginx:1.29"
	ServiceName   = "nginx"

	validateName = "panel-nginx-validate"
)

// settle is how long Healthy watches the master after a reload. A config that
// passed `nginx -t` can still make the master exit on a runtime failure; this
// is the window in which that shows.
const settle = 750 * time.Millisecond

// Engine is the part of the Docker client the nginx service uses.
type Engine interface {
	docker.Engine
	ContainerKill(ctx context.Context, id, signal string) error
	Exec(ctx context.Context, id string, opts docker.ExecOptions) (docker.ExecResult, error)
	RunOnce(ctx context.Context, name string, cfg docker.ContainerConfig) (docker.ExecResult, error)
	ImageBuild(ctx context.Context, opts docker.BuildOptions, progress docker.Progress) (docker.Image, error)
}

// Spec is the nginx container: host network, the config tree read-only, the
// site logs writable, web roots read-only, the PHP socket directory, the site certificates and
// the ACME webroot read-only, every path identical on the host and
// in the container. Nothing is mounted over a directory the image owns.
func Spec(p Paths) docker.Spec {
	return docker.Spec{
		Name:       ContainerName,
		Image:      Image,
		Role:       "nginx",
		Entrypoint: []string{"nginx"},
		Args:       []string{"-c", p.ConfPath(), "-g", "daemon off;"},
		StopSignal: "SIGQUIT",
		Mounts: []docker.Mount{
			{Path: p.Dir(), ReadOnly: true},
			{Path: p.SiteLogDir()},
			{Path: p.SitesRoot(), ReadOnly: true},
			// Read-write: connecting to a Unix socket needs write access to it.
			{Path: p.RunDir()},
			// Certificates and the challenge webroot, never SSLDir itself: that
			// holds the panel's key and the ACME account key.
			{Path: p.SiteCertsDir(), ReadOnly: true},
			{Path: p.ACMEWebroot(), ReadOnly: true},
		},
	}
}

// Dockerfile is the derived image: the official one plus the `www` group and
// user at the fixed IDs. The base is a Debian image, which has groupadd.
func Dockerfile() []byte {
	return fmt.Appendf(nil,
		"FROM %s\nRUN groupadd -g %d %s && useradd -u %d -g %s -M -d /nonexistent -s /usr/sbin/nologin %s\n",
		BaseImage, WWWGID, WWWUser, WWWUID, WWWUser, WWWUser)
}

// ImagePresent reports whether the derived image exists locally.
func ImagePresent(ctx context.Context, eng Engine) (bool, error) {
	if _, err := eng.ImageInspect(ctx, Image); err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// BuildImage builds the derived image. It is a job body: progress is the
// job's output.
func BuildImage(ctx context.Context, eng Engine, progress docker.Progress) error {
	_, err := eng.ImageBuild(ctx, docker.BuildOptions{
		Tag: Image, Dockerfile: Dockerfile(), Pull: true,
		Labels: map[string]string{"panel.managed": "true"},
	}, progress)
	return err
}

// Service is nginx as a confedit.Service: it validates with `nginx -t`,
// reloads by signalling the master, and checks the container is still up.
type Service struct {
	eng  Engine
	p    Paths
	wait time.Duration
}

// NewService returns the nginx service.
func NewService(eng Engine, p Paths) *Service {
	return &Service{eng: eng, p: p, wait: settle}
}

// Name implements confedit.Service.
func (s *Service) Name() string { return ServiceName }

// Validate runs `nginx -t` against the files on disk: by exec in the running
// container, or in a one-shot container with the same mounts when the service
// is down, so a config can be checked before nginx is first started.
func (s *Service) Validate(ctx context.Context) (string, error) {
	return validate(ctx, s.eng, s.p)
}

func validate(ctx context.Context, eng Engine, p Paths) (string, error) {
	test := []string{"nginx", "-t", "-c", p.ConfPath()}

	var res docker.ExecResult
	c, err := eng.ContainerInspect(ctx, ContainerName)
	switch {
	case err == nil && c.State.Running:
		res, err = eng.Exec(ctx, ContainerName, docker.ExecOptions{Cmd: test})
	case err == nil || errors.Is(err, docker.ErrNotFound):
		cfg := Spec(p).Config()
		cfg.Entrypoint, cfg.Cmd = test[:1], test[1:]
		// A leftover from a daemon that died mid-validation would make the name
		// taken; removing it first costs nothing when it is not there.
		_ = eng.ContainerRemove(ctx, validateName, true)
		res, err = eng.RunOnce(ctx, validateName, cfg)
	}
	if err != nil {
		return "", fmt.Errorf("nginx -t could not be run: %w", err)
	}
	if res.ExitCode != 0 {
		return res.Output(), fmt.Errorf("nginx -t rejected the configuration (exit %d)", res.ExitCode)
	}
	return res.Output(), nil
}

// Reload signals the master process of the running container. It refuses when
// the container is not running: a reload that silently did nothing would let an
// apply report success for a config nothing loaded.
func (s *Service) Reload(ctx context.Context) error {
	c, err := s.eng.ContainerInspect(ctx, ContainerName)
	if err != nil {
		return fmt.Errorf("nginx reload: %w", err)
	}
	if !c.State.Running {
		return fmt.Errorf("nginx reload: container %s is %s", ContainerName, c.State.Status)
	}
	if err := s.eng.ContainerKill(ctx, ContainerName, "HUP"); err != nil {
		return fmt.Errorf("nginx reload: %w", err)
	}
	return nil
}

// Healthy checks that the container is running and, after a short settle, that
// it is the same process: a master that exited on the new config and was
// restarted by the restart policy shows as a new start time.
func (s *Service) Healthy(ctx context.Context) error {
	before, err := s.running(ctx)
	if err != nil {
		return err
	}
	select {
	case <-time.After(s.wait):
	case <-ctx.Done():
		return ctx.Err()
	}
	after, err := s.running(ctx)
	if err != nil {
		return err
	}
	if after.State.Pid != before.State.Pid || !after.State.StartedAt.Equal(before.State.StartedAt) {
		return errors.New("nginx restarted right after the reload: the new configuration made the master exit")
	}
	return nil
}

func (s *Service) running(ctx context.Context) (docker.Container, error) {
	c, err := s.eng.ContainerInspect(ctx, ContainerName)
	if err != nil {
		return c, fmt.Errorf("nginx health: %w", err)
	}
	if !c.State.Running || c.State.Restarting {
		return c, fmt.Errorf("nginx health: container is %s", c.State.Status)
	}
	return c, nil
}

// ValidateOnly is the nginx service for changes made while the container is not
// yet running (boot): it validates in a one-shot container and does not reload.
type ValidateOnly struct {
	eng Engine
	p   Paths
}

// NewValidateOnly returns the validate-only service. It shares the apply lock
// with Service because it has the same name.
func NewValidateOnly(eng Engine, p Paths) *ValidateOnly { return &ValidateOnly{eng: eng, p: p} }

// Name implements confedit.Service.
func (v *ValidateOnly) Name() string { return ServiceName }

// Validate implements confedit.Service.
func (v *ValidateOnly) Validate(ctx context.Context) (string, error) {
	return validate(ctx, v.eng, v.p)
}

// Reload does nothing: the container is about to be started with these files.
func (v *ValidateOnly) Reload(context.Context) error { return nil }

// Healthy does nothing for the same reason.
func (v *ValidateOnly) Healthy(context.Context) error { return nil }

var (
	_ confedit.Service = (*Service)(nil)
	_ confedit.Service = (*ValidateOnly)(nil)
)

// Runtime manages the nginx container's existence: the image behind it and the
// container itself.
type Runtime struct {
	eng Engine
	p   Paths
}

// NewRuntime returns the nginx runtime.
func NewRuntime(eng Engine, p Paths) *Runtime { return &Runtime{eng: eng, p: p} }

// ImagePresent reports whether the derived image exists locally.
func (r *Runtime) ImagePresent(ctx context.Context) (bool, error) { return ImagePresent(ctx, r.eng) }

// EnsureContainer creates, recreates or starts the container so it matches Spec.
func (r *Runtime) EnsureContainer(ctx context.Context) (docker.EnsureResult, error) {
	return docker.Ensure(ctx, r.eng, Spec(r.p), r.p.Root)
}

// State is the nginx container as Docker reports it. The bool is false when
// there is no such container.
func (s *Service) State(ctx context.Context) (docker.Container, bool, error) {
	c, err := s.eng.ContainerInspect(ctx, ContainerName)
	if errors.Is(err, docker.ErrNotFound) {
		return docker.Container{}, false, nil
	}
	if err != nil {
		return docker.Container{}, false, err
	}
	return c, true, nil
}
