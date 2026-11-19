package php

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/native"
)

// settle is how long Healthy watches the master after a reload, for the same
// reason as nginx: a config that passed the test can still make it exit.
const settle = 750 * time.Millisecond

// stopTimeout is how long systemd lets FPM finish its requests after SIGQUIT.
// The unit says the same; this is the budget the panel waits for.
const stopTimeout = 25 * time.Second

// Runner runs a host command and returns its combined output.
type Runner = native.Runner

// ExecRunner runs commands on this host.
type ExecRunner = native.ExecRunner

// UnitName is the systemd unit that runs one version's FPM master.
func UnitName(ver string) string { return "panel-php-" + ver + ".service" }

// unit is one version's unit, as the shared helpers name it.
func unit(ver string) native.Unit { return native.Unit{Product: product, Name: UnitName(ver)} }

// Engine is the part of the Docker client PHP uses. The native runtime needs none
// of it; it stays while the manager still builds images (step 4).
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

// scanDirEnv makes PHP read the panel's conf.d after the build's own: a leading
// colon keeps the compiled-in directory, where the build enables its extensions.
func scanDirEnv(p Paths, ver string) string { return "PHP_INI_SCAN_DIR=:" + p.ConfDir(ver) }

// Spec is the container of one version running image. FPM is started on the
// panel's own php-fpm.conf, so the image's php-fpm.d (and with it the `www`
// pool that listens on TCP 9000) is never read, and nothing is mounted over it.
// The master runs as root, as the image intends, and each pool drops to `www`.
func Spec(p Paths, ver, image string) docker.Spec {
	spec := docker.Spec{
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
	// Folders the operator allowed as web roots. Read-write, as the sites tree
	// is: an application under PHP writes its uploads and caches into its root.
	for _, root := range p.Roots.List() {
		spec.Mounts = append(spec.Mounts, docker.Mount{Path: root})
	}
	return spec
}

// Service is one PHP version as a confedit.Service: it validates with the
// version's own php-fpm and php binaries on the host, reloads with USR2 and
// checks the master is still the same process.
type Service struct {
	p    Paths
	run  Runner
	ver  string
	wait time.Duration
	rt   *Runtime
	// start makes Reload bring the version up when its unit is not running.
	// Changes that give the version its first site use it; a settings edit does
	// not, so a version nobody uses stays down (step 2.7).
	start bool
}

// NewService returns the service of a version. With start set, a reload starts
// the version's unit when it is not running.
func NewService(run Runner, p Paths, ver string, start bool) *Service {
	return &Service{p: p, run: run, ver: ver, wait: settle, start: start, rt: NewRuntime(p, run, ver)}
}

// WithUnitDir writes the unit somewhere other than systemd's own directory.
// Tests use it; production does not.
func (s *Service) WithUnitDir(dir string) *Service {
	s.rt.WithUnitDir(dir)
	return s
}

// Name implements confedit.Service.
func (s *Service) Name() string { return ServiceName(s.ver) }

// iniTrouble matches what PHP prints when php.ini or a conf.d file does not
// parse or names an extension it cannot load. Both commands of Validate report
// those and still exit 0 (confirmed against php:8.3-fpm and a native 8.3 build),
// so the exit code alone would let a broken ini go live.
var iniTrouble = regexp.MustCompile(`PHP:\s+syntax error|PHP Startup:|PHP (Warning|Fatal error|Parse error)|\b(ERROR|ALERT):`)

// Validate checks the files on disk with two commands run on the host: `php-fpm
// -t` for the FPM config and the ini syntax, then a PHP start-up for what only
// loading the ini shows, such as an extension that does not exist. It needs no
// running unit, so a configuration can be checked before the version is first
// started.
func (s *Service) Validate(ctx context.Context) (string, error) {
	runEnv, ok := s.run.(native.EnvRunner)
	if !ok {
		return "", fmt.Errorf("php %s: the runner cannot set PHP_INI_SCAN_DIR, so conf.d would be ignored", s.ver)
	}
	ini := s.p.IniPath(s.ver)
	env := []string{scanDirEnv(s.p, s.ver)}
	var out strings.Builder
	for _, test := range []struct {
		what string
		bin  string
		args []string
	}{
		{"php-fpm", s.p.Binary(s.ver), []string{"-t", "-y", s.p.FPMConf(s.ver), "-c", ini}},
		{"php", s.p.CLIBinary(s.ver), []string{"-c", ini, "-d", "display_startup_errors=1", "-d", "log_errors=0", "-r", ""}},
	} {
		b, err := runEnv.RunEnv(ctx, env, test.bin, test.args...)
		out.Write(b)
		if err != nil {
			if code, ran := native.ExitCode(err); ran {
				return out.String(), fmt.Errorf("%s rejected the configuration (exit %d)", test.what, code)
			}
			return out.String(), fmt.Errorf("%s could not be run: %w", test.what, err)
		}
		if iniTrouble.MatchString(string(b)) {
			return out.String(), fmt.Errorf("%s reported a problem with the configuration", test.what)
		}
	}
	return out.String(), nil
}

// Reload asks systemd to signal the master. It refuses when the unit is not
// running: a reload that silently did nothing would let an apply report success
// for a config nothing loaded. A service built with start brings the version up
// instead, which is what a first site does.
func (s *Service) Reload(ctx context.Context) error {
	st, _, err := s.Status(ctx)
	if err != nil {
		return fmt.Errorf("php %s reload: %w", s.ver, err)
	}
	if st.Active {
		if out, err := s.run.Run(ctx, "systemctl", "reload", UnitName(s.ver)); err != nil {
			return fmt.Errorf("php %s reload: %w: %s", s.ver, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if !s.start {
		return fmt.Errorf("php %s reload: %s is %s", s.ver, UnitName(s.ver), st.State)
	}
	if _, err := s.rt.EnsureRunning(ctx); err != nil {
		return fmt.Errorf("php %s start: %w", s.ver, err)
	}
	return nil
}

// Healthy checks the unit is running and, after a short settle, that it is the
// same process: USR2 re-executes the master in place, so a new PID or start time
// means the master exited on the new config and systemd restarted it.
func (s *Service) Healthy(ctx context.Context) error {
	if !s.start {
		st, _, err := s.Status(ctx)
		if err != nil {
			return fmt.Errorf("php %s health: %w", s.ver, err)
		}
		if !st.Active {
			// Nothing was reloaded for a version nobody uses; that is not unhealthy.
			return nil
		}
	}
	return native.Stable(ctx, s.run, unit(s.ver), s.wait)
}

// Status is the version's unit as systemd reports it. The bool is false when the
// unit does not exist yet.
func (s *Service) Status(ctx context.Context) (native.Status, bool, error) {
	st, err := native.StatusOf(ctx, s.run, unit(s.ver))
	return st, st.Loaded, err
}

// State is the unit's status in the container shape the manager and the HTTP
// layer read today. Step 4 moves both callers onto Status and this goes away.
func (s *Service) State(ctx context.Context) (docker.Container, bool, error) {
	st, found, err := s.Status(ctx)
	if err != nil || !found {
		return docker.Container{}, false, err
	}
	word := "exited"
	switch {
	case st.Active:
		word = "running"
	case st.State == "activating":
		word = "restarting"
	case st.State == "failed":
		word = "failed"
	}
	return docker.Container{
		Name:  UnitName(s.ver),
		State: docker.ContainerState{Status: word, Running: st.Active, Pid: st.PID},
	}, true, nil
}

var _ confedit.Service = (*Service)(nil)
