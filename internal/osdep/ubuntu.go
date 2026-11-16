package osdep

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

const (
	defaultOSReleasePath = "/etc/os-release"
	defaultPasswdPath    = "/etc/passwd"
	defaultGroupPath     = "/etc/group"

	dockerKeyringsDir = "/etc/apt/keyrings"
	dockerKeyringPath = dockerKeyringsDir + "/docker.asc"
	dockerSourcesPath = "/etc/apt/sources.list.d/docker.list"
	dockerRepoURL     = "https://download.docker.com/linux/ubuntu"
	dockerGPGURL      = dockerRepoURL + "/gpg"
)

// UbuntuEnv is the environment every Ubuntu package command runs with: dpkg must
// never stop to ask a question the panel cannot answer.
var UbuntuEnv = []string{"DEBIAN_FRONTEND=noninteractive"}

// Ubuntu implements OS for Ubuntu and for derivatives that keep ID=ubuntu in
// /etc/os-release. Debian is deliberately not matched by ID_LIKE: its packages and
// Docker repository differ, and a silent match would be worse than a clear refusal.
type Ubuntu struct {
	run Runner

	osReleasePath string
	passwdPath    string
	groupPath     string

	writeFile func(name string, data []byte, perm os.FileMode) error
}

var _ OS = (*Ubuntu)(nil)

// NewUbuntu returns the Ubuntu implementation. Production passes an ExecRunner
// carrying UbuntuEnv; tests pass a fake runner that records argv.
func NewUbuntu(r Runner) *Ubuntu {
	return &Ubuntu{
		run:           r,
		osReleasePath: defaultOSReleasePath,
		passwdPath:    defaultPasswdPath,
		groupPath:     defaultGroupPath,
		writeFile:     os.WriteFile,
	}
}

// DetectDistro reads /etc/os-release and returns the distribution, or an error
// wrapping ErrUnknownDistro for anything the panel does not implement.
func (u *Ubuntu) DetectDistro() (Distro, error) {
	values, err := u.osRelease()
	if err != nil {
		return "", err
	}
	id := strings.ToLower(values["ID"])
	if Distro(id) == DistroUbuntu {
		return DistroUbuntu, nil
	}
	return "", fmt.Errorf("%w: ID=%q in %s", ErrUnknownDistro, id, u.osReleasePath)
}

func (u *Ubuntu) osRelease() (map[string]string, error) {
	data, err := os.ReadFile(u.osReleasePath)
	if err != nil {
		return nil, fmt.Errorf("osdep: read %s: %w", u.osReleasePath, err)
	}
	values, err := parseOSRelease(data)
	if err != nil {
		return nil, fmt.Errorf("osdep: parse %s: %w", u.osReleasePath, err)
	}
	return values, nil
}

// InstallPackages refreshes the package index and installs pkgs.
func (u *Ubuntu) InstallPackages(ctx context.Context, pkgs []string) error {
	if len(pkgs) == 0 {
		return errors.New("osdep: InstallPackages: no packages")
	}
	if err := u.run.Run(ctx, "apt-get", "update"); err != nil {
		return err
	}
	args := append([]string{"install", "-y", "--no-install-recommends"}, pkgs...)
	return u.run.Run(ctx, "apt-get", args...)
}

// EnsureUser makes name exist with exactly this uid and gid. An existing user with
// a different uid or gid is an error rather than a silent change: those numbers
// already own files, and moving them is the operator's decision, not an install step.
func (u *Ubuntu) EnsureUser(ctx context.Context, name string, uid, gid int) error {
	if name == "" || strings.ContainsAny(name, ":\n/") {
		return fmt.Errorf("osdep: EnsureUser: invalid user name %q", name)
	}
	if uid <= 0 || gid <= 0 {
		return fmt.Errorf("osdep: EnsureUser(%q): uid %d and gid %d must both be positive", name, uid, gid)
	}

	users, err := readPasswd(u.passwdPath)
	if err != nil {
		return err
	}
	if have, ok := users[name]; ok {
		if have.uid == uid && have.gid == gid {
			return nil
		}
		return fmt.Errorf("osdep: user %q exists with uid %d gid %d, want uid %d gid %d",
			name, have.uid, have.gid, uid, gid)
	}

	groups, err := readGroups(u.groupPath)
	if err != nil {
		return err
	}
	groupExists := false
	for _, g := range groups {
		switch {
		case g.name == name && g.gid == gid:
			groupExists = true
		case g.name == name:
			return fmt.Errorf("osdep: group %q exists with gid %d, want gid %d", name, g.gid, gid)
		case g.gid == gid:
			return fmt.Errorf("osdep: gid %d is already used by group %q", gid, g.name)
		}
	}

	if !groupExists {
		if err := u.run.Run(ctx, "groupadd", "--gid", strconv.Itoa(gid), name); err != nil {
			return err
		}
	}
	return u.run.Run(ctx, "useradd",
		"--uid", strconv.Itoa(uid),
		"--gid", strconv.Itoa(gid),
		"--no-create-home",
		"--home-dir", "/www",
		"--shell", "/usr/sbin/nologin",
		name,
	)
}

// EnsureDocker installs Docker Engine from Docker's own repository. Any failure —
// unreachable key, unreachable repository, a package that does not exist — is
// returned as-is. There is no fallback to the distribution's docker.io package:
// a panel that quietly installed a different engine would fail later and further
// from the cause (development plan, M0 risks).
func (u *Ubuntu) EnsureDocker(ctx context.Context) error {
	values, err := u.osRelease()
	if err != nil {
		return err
	}
	codename := values["VERSION_CODENAME"]
	if codename == "" {
		codename = values["UBUNTU_CODENAME"]
	}
	if codename == "" {
		return fmt.Errorf("osdep: %s has no VERSION_CODENAME; cannot address the Docker repository", u.osReleasePath)
	}
	arch, err := dockerArch(runtime.GOARCH)
	if err != nil {
		return err
	}

	repoSetup := []struct {
		name string
		args []string
	}{
		{"apt-get", []string{"update"}},
		{"apt-get", []string{"install", "-y", "--no-install-recommends", "ca-certificates", "curl", "gnupg"}},
		{"install", []string{"-m", "0755", "-d", dockerKeyringsDir}},
		{"curl", []string{"--fail", "--silent", "--show-error", "--location",
			"--output", dockerKeyringPath, dockerGPGURL}},
		{"chmod", []string{"a+r", dockerKeyringPath}},
	}
	for _, step := range repoSetup {
		if err := u.run.Run(ctx, step.name, step.args...); err != nil {
			return fmt.Errorf("osdep: docker repository setup: %w", err)
		}
	}

	sources := fmt.Sprintf("deb [arch=%s signed-by=%s] %s %s stable\n",
		arch, dockerKeyringPath, dockerRepoURL, codename)
	if err := u.writeFile(dockerSourcesPath, []byte(sources), 0o644); err != nil {
		return fmt.Errorf("osdep: write %s: %w", dockerSourcesPath, err)
	}

	install := []struct {
		name string
		args []string
	}{
		{"apt-get", []string{"update"}},
		{"apt-get", []string{"install", "-y", "--no-install-recommends",
			"docker-ce", "docker-ce-cli", "containerd.io",
			"docker-buildx-plugin", "docker-compose-plugin"}},
		{"systemctl", []string{"enable", "--now", "docker"}},
	}
	for _, step := range install {
		if err := u.run.Run(ctx, step.name, step.args...); err != nil {
			return fmt.Errorf("osdep: docker install: %w", err)
		}
	}
	return nil
}

// dockerArch maps the build's architecture to the name Docker's repository uses.
// The binary runs on the host it was built for, so GOARCH is the host's answer and
// no command output has to be parsed.
func dockerArch(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "amd64", nil
	case "arm64":
		return "arm64", nil
	case "386":
		return "i386", nil
	case "arm":
		return "armhf", nil
	}
	return "", fmt.Errorf("osdep: no Docker repository architecture for GOARCH %q", goarch)
}

// MaskFirewallServices stops and masks ufw so it cannot overwrite the panel's
// iptables chains. Unused until the firewall milestone.
func (u *Ubuntu) MaskFirewallServices(ctx context.Context) error {
	if err := u.run.Run(ctx, "systemctl", "stop", "ufw"); err != nil {
		return fmt.Errorf("osdep: stop ufw: %w", err)
	}
	if err := u.run.Run(ctx, "systemctl", "mask", "ufw"); err != nil {
		return fmt.Errorf("osdep: mask ufw: %w", err)
	}
	return nil
}

// SELinux reports nothing on Ubuntu, which does not ship SELinux by default.
func (u *Ubuntu) SELinux() SELinuxStatus { return SELinuxStatus{} }
