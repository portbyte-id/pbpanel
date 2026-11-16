package osdep

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Distro identifies the host distribution. Naming one is allowed only inside this
// package; every other package calls OS instead.
type Distro string

// DistroUbuntu covers Ubuntu LTS and derivatives that keep ID=ubuntu in os-release.
const DistroUbuntu Distro = "ubuntu"

// ErrUnknownDistro is returned for an os-release the panel does not implement.
var ErrUnknownDistro = errors.New("osdep: unsupported distribution")

// SELinuxStatus is the host's SELinux state. On Ubuntu both fields are false.
type SELinuxStatus struct {
	// Supported reports that SELinux is built into the kernel and a policy is loaded.
	Supported bool
	// Enforcing reports that the loaded policy is being enforced.
	Enforcing bool
}

// OS is the distro boundary: the only way the rest of the panel touches packages,
// users, Docker, firewall services and SELinux. Adding a distribution means adding
// an implementation here, not editing callers.
type OS interface {
	DetectDistro() (Distro, error)
	InstallPackages(ctx context.Context, pkgs []string) error
	EnsureUser(ctx context.Context, name string, uid, gid int) error
	EnsureDocker(ctx context.Context) error
	// MaskFirewallServices stops and masks the distribution's own firewall service
	// so it cannot fight the panel's iptables chains. Unused until M4.
	MaskFirewallServices(ctx context.Context) error
	SELinux() SELinuxStatus
}

// Runner runs one external command. Implementations receive an argument vector and
// never a shell line; the panel does not build shell strings (development plan §1.4).
//
// Every later step tests against this seam: a fake Runner records argv and asserts
// the exact command an implementation emits.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// ExecRunner runs commands with exec.CommandContext. Env entries are appended to
// the inherited process environment.
type ExecRunner struct {
	Env []string
}

// Run implements Runner.
func (r *ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := r.command(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("osdep: %s %s: %w: %s", name, strings.Join(args, " "), err, msg)
		}
		return fmt.Errorf("osdep: %s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func (r *ExecRunner) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(r.Env) > 0 {
		cmd.Env = append(cmd.Environ(), r.Env...)
	}
	return cmd
}

// DefaultRunner is the production runner: a real executor carrying the environment
// every package command needs, so dpkg never blocks on a prompt the panel cannot see.
// Callers build their OS as NewUbuntu(DefaultRunner()) and then call DetectDistro.
func DefaultRunner() Runner { return &ExecRunner{Env: UbuntuEnv} }
