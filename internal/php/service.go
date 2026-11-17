package php

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
)

// settle is how long Healthy watches the master after a reload, for the same
// reason as nginx: a config that passed the test can still make it exit.
const settle = 750 * time.Millisecond

// Engine is the part of the Docker client PHP uses.
type Engine interface {
	docker.Engine
	ContainerKill(ctx context.Context, id, signal string) error
	Exec(ctx context.Context, id string, opts docker.ExecOptions) (docker.ExecResult, error)
	RunOnce(ctx context.Context, name string, cfg docker.ContainerConfig) (docker.ExecResult, error)
	ImagePull(ctx context.Context, ref string, progress docker.Progress) (docker.Image, error)
	ImageBuild(ctx context.Context, opts docker.BuildOptions, progress docker.Progress) (docker.Image, error)
	ImageTag(ctx context.Context, src, dst string) error
	ImageRemove(ctx context.Context, ref string) error
}

// ImageFunc names the image a version's container runs now. It is a function so
// that a swap takes effect on the next start without rebuilding the service.
type ImageFunc func(ctx context.Context) (string, error)

// scanDirEnv makes PHP read the panel's conf.d after the image's own: a
// leading colon keeps the compiled-in directory, where the image enables its
// extensions.
func scanDirEnv(p Paths, ver string) string { return "PHP_INI_SCAN_DIR=:" + p.ConfDir(ver) }

// Spec is the container of one version running image. FPM is started on the
// panel's own php-fpm.conf, so the image's php-fpm.d (and with it the `www`
// pool that listens on TCP 9000) is never read, and nothing is mounted over it.
// The master runs as root, as the image intends, and each pool drops to `www`.
func Spec(p Paths, ver, image string) docker.Spec {
	return docker.Spec{
		Name:       ContainerName(ver),
		Image:      image,
		Role:       "php-" + ver,
		Entrypoint: []string{"php-fpm"},
		Args:       []string{"-F", "-y", p.FPMConf(ver), "-c", p.IniPath(ver)},
		Env:        []string{scanDirEnv(p, ver)},
		StopSignal: "SIGQUIT",
		Mounts: []docker.Mount{
			{Path: p.Dir(ver), ReadOnly: true},
			{Path: p.RunDir()},
			{Path: p.SiteLogDir()},
			{Path: p.SitesRoot()},
		},
	}
}

// Service is one PHP version as a confedit.Service: it validates with
// `php-fpm -t`, reloads with USR2 and checks the container is still up.
type Service struct {
	eng   Engine
	p     Paths
	ver   string
	wait  time.Duration
	image ImageFunc
	// start makes Reload create or start the container when it is not running.
	// Changes that give the version its first site use it; a settings edit does
	// not, so a version nobody uses stays down (step 2.7).
	start bool
}

// NewService returns the service of a version. With start set, a reload brings
// the container up if it is not running. image names the build to run when a
// container has to be created.
func NewService(eng Engine, p Paths, ver string, start bool, image ImageFunc) *Service {
	return &Service{eng: eng, p: p, ver: ver, wait: settle, start: start, image: image}
}

// Name implements confedit.Service.
func (s *Service) Name() string { return ServiceName(s.ver) }

// iniTrouble matches what PHP prints when php.ini or a conf.d file does not
// parse or names an extension it cannot load. Both commands of Validate report
// those and still exit 0 (confirmed against php:8.3-fpm), so the exit code
// alone would let a broken ini go live.
var iniTrouble = regexp.MustCompile(`PHP:\s+syntax error|PHP Startup:|PHP (Warning|Fatal error|Parse error)|\b(ERROR|ALERT):`)

// Validate checks the files on disk with two commands: `php-fpm -t` for the
// FPM config and the ini syntax, then a PHP start-up for what only loading the
// ini shows, such as an extension that does not exist. They run by exec in the
// running container, or in a one-shot container with the same mounts when it
// is down.
func (s *Service) Validate(ctx context.Context) (string, error) {
	ini := s.p.IniPath(s.ver)
	var out string
	for _, test := range [][]string{
		{"php-fpm", "-t", "-y", s.p.FPMConf(s.ver), "-c", ini},
		{"php", "-c", ini, "-d", "display_startup_errors=1", "-d", "log_errors=0", "-r", ""},
	} {
		res, err := s.run(ctx, test)
		if err != nil {
			return out, fmt.Errorf("%s could not be run: %w", test[0], err)
		}
		out += res.Output()
		if res.ExitCode != 0 {
			return out, fmt.Errorf("%s rejected the configuration (exit %d)", test[0], res.ExitCode)
		}
		if iniTrouble.MatchString(res.Output()) {
			return out, fmt.Errorf("%s reported a problem with the configuration", test[0])
		}
	}
	return out, nil
}

func (s *Service) run(ctx context.Context, cmd []string) (docker.ExecResult, error) {
	name := ContainerName(s.ver)
	c, err := s.eng.ContainerInspect(ctx, name)
	switch {
	case err == nil && c.State.Running:
		return s.eng.Exec(ctx, name, docker.ExecOptions{Cmd: cmd, Env: []string{scanDirEnv(s.p, s.ver)}})
	case err == nil || errors.Is(err, docker.ErrNotFound):
		img, err := s.image(ctx)
		if err != nil {
			return docker.ExecResult{}, err
		}
		cfg := Spec(s.p, s.ver, img).Config()
		cfg.Entrypoint, cfg.Cmd = cmd[:1], cmd[1:]
		// A leftover from a daemon that died mid-validation would hold the name.
		_ = s.eng.ContainerRemove(ctx, name+"-validate", true)
		return s.eng.RunOnce(ctx, name+"-validate", cfg)
	}
	return docker.ExecResult{}, err
}

// Reload sends USR2 to the master of the running container. When the container
// is not running, a starting service brings it up on the files just written; a
// plain one does nothing, because nothing is running the old config either.
func (s *Service) Reload(ctx context.Context) error {
	name := ContainerName(s.ver)
	c, err := s.eng.ContainerInspect(ctx, name)
	switch {
	case err == nil && c.State.Running:
		if err := s.eng.ContainerKill(ctx, name, "USR2"); err != nil {
			return fmt.Errorf("php %s reload: %w", s.ver, err)
		}
		return nil
	case err != nil && !errors.Is(err, docker.ErrNotFound):
		return fmt.Errorf("php %s reload: %w", s.ver, err)
	}
	if !s.start {
		return nil
	}
	img, err := s.image(ctx)
	if err != nil {
		return fmt.Errorf("php %s start: %w", s.ver, err)
	}
	if _, err := docker.Ensure(ctx, s.eng, Spec(s.p, s.ver, img), s.p.Root); err != nil {
		return fmt.Errorf("php %s start: %w", s.ver, err)
	}
	return nil
}

// Healthy checks that the container is running and, after a short settle, that
// it was not restarted: USR2 re-executes the master in place, so a new start
// time means the master exited on the new config.
func (s *Service) Healthy(ctx context.Context) error {
	before, found, err := s.State(ctx)
	if err != nil {
		return fmt.Errorf("php %s health: %w", s.ver, err)
	}
	if !found || !before.State.Running {
		if s.start {
			return fmt.Errorf("php %s health: the container is not running", s.ver)
		}
		return nil
	}
	select {
	case <-time.After(s.wait):
	case <-ctx.Done():
		return ctx.Err()
	}
	after, found, err := s.State(ctx)
	if err != nil {
		return fmt.Errorf("php %s health: %w", s.ver, err)
	}
	if !found || !after.State.Running || after.State.Restarting {
		return fmt.Errorf("php %s health: the container stopped right after the reload", s.ver)
	}
	if !after.State.StartedAt.Equal(before.State.StartedAt) {
		return fmt.Errorf("php %s restarted right after the reload: the new configuration made FPM exit", s.ver)
	}
	return nil
}

// State is the version's container as Docker reports it. The bool is false
// when there is no such container.
func (s *Service) State(ctx context.Context) (docker.Container, bool, error) {
	c, err := s.eng.ContainerInspect(ctx, ContainerName(s.ver))
	if errors.Is(err, docker.ErrNotFound) {
		return docker.Container{}, false, nil
	}
	if err != nil {
		return docker.Container{}, false, err
	}
	return c, true, nil
}

var _ confedit.Service = (*Service)(nil)
