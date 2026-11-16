package osdep

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// call is one recorded argv.
type call struct {
	name string
	args []string
}

func (c call) String() string { return c.name + " " + strings.Join(c.args, " ") }

// fakeRunner records every command and fails the first one matching failOn.
type fakeRunner struct {
	calls  []call
	failOn string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	c := call{name: name, args: append([]string(nil), args...)}
	f.calls = append(f.calls, c)
	if f.failOn != "" && strings.HasPrefix(c.String(), f.failOn) {
		return errors.New("command failed")
	}
	return nil
}

func (f *fakeRunner) names() []string {
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.String()
	}
	return out
}

const ubuntuOSRelease = `PRETTY_NAME="Ubuntu 24.04.1 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.1 LTS (Noble Numbat)"
VERSION_CODENAME=noble
ID=ubuntu
ID_LIKE=debian
HOME_URL="https://www.ubuntu.com/"
UBUNTU_CODENAME=noble
`

const rockyOSRelease = `NAME="Rocky Linux"
VERSION="9.4 (Blue Onyx)"
ID="rocky"
ID_LIKE="rhel centos fedora"
VERSION_ID="9.4"
VERSION_CODENAME=""
`

const basePasswd = `root:x:0:0:root:/root:/bin/bash
www:x:1000:1000::/www:/usr/sbin/nologin
`

const baseGroup = `root:x:0:
www:x:1000:
`

// newTestUbuntu builds a Ubuntu backed by temp os-release, passwd and group files,
// so no test reads the machine it runs on.
func newTestUbuntu(t *testing.T, osRelease, passwd, group string, r Runner) *Ubuntu {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	u := NewUbuntu(r)
	u.osReleasePath = write("os-release", osRelease)
	u.passwdPath = write("passwd", passwd)
	u.groupPath = write("group", group)
	return u
}

func TestDetectDistroUbuntu(t *testing.T) {
	u := newTestUbuntu(t, ubuntuOSRelease, basePasswd, baseGroup, &fakeRunner{})
	got, err := u.DetectDistro()
	if err != nil {
		t.Fatalf("DetectDistro: %v", err)
	}
	if got != DistroUbuntu {
		t.Fatalf("DetectDistro = %q, want %q", got, DistroUbuntu)
	}
}

func TestDetectDistroRejectsUnknown(t *testing.T) {
	u := newTestUbuntu(t, rockyOSRelease, basePasswd, baseGroup, &fakeRunner{})
	got, err := u.DetectDistro()
	if err == nil {
		t.Fatalf("DetectDistro on Rocky = %q, want an error", got)
	}
	if !errors.Is(err, ErrUnknownDistro) {
		t.Fatalf("error %v does not wrap ErrUnknownDistro", err)
	}
	if !strings.Contains(err.Error(), "rocky") {
		t.Fatalf("error %q does not name the distro it refused", err)
	}
}

func TestDetectDistroMissingFile(t *testing.T) {
	u := NewUbuntu(&fakeRunner{})
	u.osReleasePath = filepath.Join(t.TempDir(), "absent")
	if _, err := u.DetectDistro(); err == nil {
		t.Fatal("DetectDistro with a missing os-release returned no error")
	}
}

func TestInstallPackagesArgv(t *testing.T) {
	r := &fakeRunner{}
	u := newTestUbuntu(t, ubuntuOSRelease, basePasswd, baseGroup, r)

	if err := u.InstallPackages(context.Background(), []string{"ca-certificates", "curl"}); err != nil {
		t.Fatalf("InstallPackages: %v", err)
	}
	want := []string{
		"apt-get update",
		"apt-get install -y --no-install-recommends ca-certificates curl",
	}
	if got := r.names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
}

func TestInstallPackagesRejectsEmpty(t *testing.T) {
	r := &fakeRunner{}
	u := newTestUbuntu(t, ubuntuOSRelease, basePasswd, baseGroup, r)
	if err := u.InstallPackages(context.Background(), nil); err == nil {
		t.Fatal("InstallPackages(nil) returned no error")
	}
	if len(r.calls) != 0 {
		t.Fatalf("InstallPackages(nil) ran %v", r.names())
	}
}

func TestInstallPackagesStopsOnFailure(t *testing.T) {
	r := &fakeRunner{failOn: "apt-get update"}
	u := newTestUbuntu(t, ubuntuOSRelease, basePasswd, baseGroup, r)
	if err := u.InstallPackages(context.Background(), []string{"curl"}); err == nil {
		t.Fatal("InstallPackages ignored the failing update")
	}
	if len(r.calls) != 1 {
		t.Fatalf("kept going after a failed update: %v", r.names())
	}
}

func TestEnsureUserCreatesGroupAndUser(t *testing.T) {
	r := &fakeRunner{}
	u := newTestUbuntu(t, ubuntuOSRelease, "root:x:0:0:root:/root:/bin/bash\n", "root:x:0:\n", r)

	if err := u.EnsureUser(context.Background(), "www", 1000, 1000); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	want := []string{
		"groupadd --gid 1000 www",
		"useradd --uid 1000 --gid 1000 --no-create-home --home-dir /www --shell /usr/sbin/nologin www",
	}
	if got := r.names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
}

func TestEnsureUserIsIdempotent(t *testing.T) {
	r := &fakeRunner{}
	u := newTestUbuntu(t, ubuntuOSRelease, basePasswd, baseGroup, r)
	if err := u.EnsureUser(context.Background(), "www", 1000, 1000); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("an existing user was changed: %v", r.names())
	}
}

func TestEnsureUserExistingGroupOnly(t *testing.T) {
	r := &fakeRunner{}
	u := newTestUbuntu(t, ubuntuOSRelease, "root:x:0:0:root:/root:/bin/bash\n", baseGroup, r)
	if err := u.EnsureUser(context.Background(), "www", 1000, 1000); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	want := []string{
		"useradd --uid 1000 --gid 1000 --no-create-home --home-dir /www --shell /usr/sbin/nologin www",
	}
	if got := r.names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
}

func TestEnsureUserRefusesWrongUID(t *testing.T) {
	r := &fakeRunner{}
	u := newTestUbuntu(t, ubuntuOSRelease, basePasswd, baseGroup, r)
	if err := u.EnsureUser(context.Background(), "www", 1001, 1000); err == nil {
		t.Fatal("EnsureUser accepted an existing user with a different uid")
	}
	if len(r.calls) != 0 {
		t.Fatalf("EnsureUser ran commands on a mismatch: %v", r.names())
	}
}

func TestEnsureUserRefusesTakenGID(t *testing.T) {
	r := &fakeRunner{}
	u := newTestUbuntu(t, ubuntuOSRelease, "root:x:0:0:root:/root:/bin/bash\n", baseGroup, r)
	if err := u.EnsureUser(context.Background(), "pbapps", 1001, 1000); err == nil {
		t.Fatal("EnsureUser accepted a gid another group already owns")
	}
	if len(r.calls) != 0 {
		t.Fatalf("EnsureUser ran commands on a gid clash: %v", r.names())
	}
}

func TestEnsureUserRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		uid  int
		gid  int
	}{
		{"", 1000, 1000},
		{"www:root", 1000, 1000},
		{"www", 0, 1000},
		{"www", 1000, 0},
	} {
		r := &fakeRunner{}
		u := newTestUbuntu(t, ubuntuOSRelease, basePasswd, baseGroup, r)
		if err := u.EnsureUser(context.Background(), tc.name, tc.uid, tc.gid); err == nil {
			t.Errorf("EnsureUser(%q, %d, %d) returned no error", tc.name, tc.uid, tc.gid)
		}
		if len(r.calls) != 0 {
			t.Errorf("EnsureUser(%q, %d, %d) ran %v", tc.name, tc.uid, tc.gid, r.names())
		}
	}
}

func TestEnsureDockerArgv(t *testing.T) {
	r := &fakeRunner{}
	u := newTestUbuntu(t, ubuntuOSRelease, basePasswd, baseGroup, r)

	var sourcesPath, sourcesBody string
	u.writeFile = func(name string, data []byte, perm os.FileMode) error {
		sourcesPath, sourcesBody = name, string(data)
		if perm != 0o644 {
			t.Errorf("sources.list.d file mode = %o, want 644", perm)
		}
		return nil
	}

	if err := u.EnsureDocker(context.Background()); err != nil {
		t.Fatalf("EnsureDocker: %v", err)
	}

	want := []string{
		"apt-get update",
		"apt-get install -y --no-install-recommends ca-certificates curl gnupg",
		"install -m 0755 -d /etc/apt/keyrings",
		"curl --fail --silent --show-error --location --output /etc/apt/keyrings/docker.asc https://download.docker.com/linux/ubuntu/gpg",
		"chmod a+r /etc/apt/keyrings/docker.asc",
		"apt-get update",
		"apt-get install -y --no-install-recommends docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin",
		"systemctl enable --now docker",
	}
	if got := r.names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}

	if sourcesPath != "/etc/apt/sources.list.d/docker.list" {
		t.Fatalf("sources path = %q", sourcesPath)
	}
	arch, err := dockerArch(runtime.GOARCH)
	if err != nil {
		t.Fatalf("dockerArch(%q): %v", runtime.GOARCH, err)
	}
	wantLine := "deb [arch=" + arch + " signed-by=/etc/apt/keyrings/docker.asc] " +
		"https://download.docker.com/linux/ubuntu noble stable\n"
	if sourcesBody != wantLine {
		t.Fatalf("sources.list.d body:\n got %q\nwant %q", sourcesBody, wantLine)
	}
}

func TestEnsureDockerFailsWhenRepoSetupFails(t *testing.T) {
	for _, failing := range []string{
		"apt-get install -y --no-install-recommends ca-certificates",
		"install -m 0755 -d /etc/apt/keyrings",
		"curl --fail",
		"chmod a+r",
	} {
		r := &fakeRunner{failOn: failing}
		u := newTestUbuntu(t, ubuntuOSRelease, basePasswd, baseGroup, r)
		u.writeFile = func(string, []byte, os.FileMode) error {
			t.Errorf("%q failed but sources.list.d was still written", failing)
			return nil
		}

		err := u.EnsureDocker(context.Background())
		if err == nil {
			t.Fatalf("EnsureDocker with %q failing returned no error", failing)
		}
		if !strings.Contains(err.Error(), "repository setup") {
			t.Errorf("error %q does not say the repository setup failed", err)
		}
		for _, c := range r.calls {
			if strings.Contains(c.String(), "docker-ce") {
				t.Fatalf("fell back to installing Docker after %q failed: %v", failing, r.names())
			}
			if strings.Contains(c.String(), "docker.io") {
				t.Fatalf("fell back to the distro package: %v", r.names())
			}
		}
	}
}

func TestEnsureDockerFailsWhenInstallFails(t *testing.T) {
	r := &fakeRunner{failOn: "apt-get install -y --no-install-recommends docker-ce"}
	u := newTestUbuntu(t, ubuntuOSRelease, basePasswd, baseGroup, r)
	u.writeFile = func(string, []byte, os.FileMode) error { return nil }

	if err := u.EnsureDocker(context.Background()); err == nil {
		t.Fatal("EnsureDocker ignored a failed docker-ce install")
	}
	for _, c := range r.calls {
		if strings.HasPrefix(c.String(), "systemctl enable") {
			t.Fatalf("enabled docker after the install failed: %v", r.names())
		}
		if strings.Contains(c.String(), "docker.io") {
			t.Fatalf("fell back to the distro package: %v", r.names())
		}
	}
}

func TestEnsureDockerFailsWithoutCodename(t *testing.T) {
	r := &fakeRunner{}
	u := newTestUbuntu(t, "ID=ubuntu\n", basePasswd, baseGroup, r)
	u.writeFile = func(string, []byte, os.FileMode) error {
		t.Error("wrote a sources file without a codename")
		return nil
	}
	if err := u.EnsureDocker(context.Background()); err == nil {
		t.Fatal("EnsureDocker accepted an os-release without a codename")
	}
	if len(r.calls) != 0 {
		t.Fatalf("ran commands before knowing the codename: %v", r.names())
	}
}

func TestEnsureDockerFailsWhenSourcesUnwritable(t *testing.T) {
	r := &fakeRunner{}
	u := newTestUbuntu(t, ubuntuOSRelease, basePasswd, baseGroup, r)
	u.writeFile = func(string, []byte, os.FileMode) error { return errors.New("read-only filesystem") }

	if err := u.EnsureDocker(context.Background()); err == nil {
		t.Fatal("EnsureDocker ignored an unwritable sources.list.d")
	}
	for _, c := range r.calls {
		if strings.Contains(c.String(), "docker-ce") {
			t.Fatalf("installed Docker although the repository was never configured: %v", r.names())
		}
	}
}

func TestMaskFirewallServicesArgv(t *testing.T) {
	r := &fakeRunner{}
	u := newTestUbuntu(t, ubuntuOSRelease, basePasswd, baseGroup, r)

	if err := u.MaskFirewallServices(context.Background()); err != nil {
		t.Fatalf("MaskFirewallServices: %v", err)
	}
	want := []string{
		"systemctl stop ufw",
		"systemctl mask ufw",
	}
	if got := r.names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
}

func TestMaskFirewallServicesStopsOnFailure(t *testing.T) {
	r := &fakeRunner{failOn: "systemctl stop ufw"}
	u := newTestUbuntu(t, ubuntuOSRelease, basePasswd, baseGroup, r)
	if err := u.MaskFirewallServices(context.Background()); err == nil {
		t.Fatal("MaskFirewallServices ignored a failed stop")
	}
	if len(r.calls) != 1 {
		t.Fatalf("masked after the stop failed: %v", r.names())
	}
}

func TestSELinuxIsNoOpOnUbuntu(t *testing.T) {
	u := newTestUbuntu(t, ubuntuOSRelease, basePasswd, baseGroup, &fakeRunner{})
	if got := u.SELinux(); got.Supported || got.Enforcing {
		t.Fatalf("SELinux() = %+v, want the zero status", got)
	}
}

func TestParseOSReleaseQuoting(t *testing.T) {
	values, err := parseOSRelease([]byte(ubuntuOSRelease))
	if err != nil {
		t.Fatalf("parseOSRelease: %v", err)
	}
	if values["ID"] != "ubuntu" {
		t.Fatalf("ID = %q", values["ID"])
	}
	if values["VERSION_CODENAME"] != "noble" {
		t.Fatalf("unquoted VERSION_CODENAME = %q", values["VERSION_CODENAME"])
	}
	if values["PRETTY_NAME"] != "Ubuntu 24.04.1 LTS" {
		t.Fatalf("quoted PRETTY_NAME = %q", values["PRETTY_NAME"])
	}
}

func TestParseOSReleaseRejectsMalformed(t *testing.T) {
	if _, err := parseOSRelease([]byte("not a key value line\n")); err == nil {
		t.Fatal("parseOSRelease accepted a line without =")
	}
}

func TestDockerArch(t *testing.T) {
	for goarch, want := range map[string]string{
		"amd64": "amd64",
		"arm64": "arm64",
		"386":   "i386",
		"arm":   "armhf",
	} {
		got, err := dockerArch(goarch)
		if err != nil {
			t.Errorf("dockerArch(%q): %v", goarch, err)
			continue
		}
		if got != want {
			t.Errorf("dockerArch(%q) = %q, want %q", goarch, got, want)
		}
	}
	if _, err := dockerArch("riscv64"); err == nil {
		t.Fatal("dockerArch accepted an unsupported architecture")
	}
}

func TestDefaultRunnerCarriesNoninteractiveEnv(t *testing.T) {
	er, ok := DefaultRunner().(*ExecRunner)
	if !ok {
		t.Fatalf("DefaultRunner() = %T, want *ExecRunner", DefaultRunner())
	}
	want := []string{"DEBIAN_FRONTEND=noninteractive"}
	if !reflect.DeepEqual(er.Env, want) {
		t.Fatalf("ExecRunner.Env = %q, want %q", er.Env, want)
	}
}

func TestExecRunnerCommandAppendsEnv(t *testing.T) {
	er := &ExecRunner{Env: UbuntuEnv}
	cmd := er.command(context.Background(), "apt-get", "update")
	if !slices.Contains(cmd.Env, "DEBIAN_FRONTEND=noninteractive") {
		t.Fatalf("command env does not carry DEBIAN_FRONTEND: %q", cmd.Env)
	}
	if got, want := cmd.Args, []string{"apt-get", "update"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("command argv = %q, want %q", got, want)
	}
}

func TestExecRunnerReportsMissingBinary(t *testing.T) {
	er := &ExecRunner{}
	err := er.Run(context.Background(), filepath.Join(t.TempDir(), "definitely-absent"), "x")
	if err == nil {
		t.Fatal("Run of a missing binary returned no error")
	}
	if !strings.Contains(err.Error(), "definitely-absent") {
		t.Fatalf("error %q does not name the command", err)
	}
}

// TestExecRunnerHelperProcess is not a real test: it is the child process the two
// tests below run, re-executing this binary so the environment and argv ExecRunner
// hands a real process can be observed without a distro tool on the test machine.
func TestExecRunnerHelperProcess(t *testing.T) {
	if os.Getenv("OSDEP_HELPER") != "1" {
		t.Skip("helper process only")
	}
	// Failing on purpose: ExecRunner only surfaces stderr when the command fails.
	fmt.Fprintf(os.Stderr, "DEBIAN_FRONTEND=%s argv=%s\n",
		os.Getenv("DEBIAN_FRONTEND"), strings.Join(os.Args[len(os.Args)-2:], "|"))
	os.Exit(3)
}

func TestExecRunnerCarriesEnvAndArgvToTheChild(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	// OSDEP_HELPER travels in the runner's Env, not the parent's: the child can only
	// see it if ExecRunner really appends the environment it was given.
	r := &ExecRunner{Env: append(UbuntuEnv, "OSDEP_HELPER=1")}
	err = r.Run(context.Background(), exe, "-test.run=TestExecRunnerHelperProcess", "a b", "$HOME")
	if err == nil {
		t.Fatal("Run returned no error although the child exited 3")
	}
	// `a b` arrives as one argument and `$HOME` unexpanded: proof the vector was
	// passed through without a shell.
	if !strings.Contains(err.Error(), "DEBIAN_FRONTEND=noninteractive argv=a b|$HOME") {
		t.Fatalf("child did not receive the env and argv as built: %v", err)
	}
}
