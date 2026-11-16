package install

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/auth"
	"github.com/portbyte-id/server-panel/internal/osdep"
	"github.com/portbyte-id/server-panel/internal/store"
)

// call is one recorded argv.
type call struct {
	name string
	args []string
}

func (c call) String() string { return c.name + " " + strings.Join(c.args, " ") }

// hostState is what the probes report. It is a plain struct so a test can put
// the host in any state, including the inconsistent ones a repair has to fix.
type hostState struct {
	userExists bool
	dockerUp   bool
	enabled    bool
	active     bool
}

// fakeRunner records every command and answers the probes from hostState.
type fakeRunner struct {
	calls []call
	state hostState
	fail  string
}

// probePrefixes are the read-only questions the skip-if-done checks ask.
var probePrefixes = []string{
	"getent passwd ",
	"systemctl is-active --quiet ",
	"systemctl is-enabled --quiet ",
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	f.calls = append(f.calls, call{name: name, args: append([]string(nil), args...)})
	joined := strings.Join(append([]string{name}, args...), " ")

	if f.fail != "" && strings.HasPrefix(joined, f.fail) {
		return errors.New("command failed")
	}

	switch joined {
	case "getent passwd www":
		if !f.state.userExists {
			return errors.New("exit status 2")
		}
	case "systemctl is-active --quiet docker":
		if !f.state.dockerUp {
			return errors.New("inactive")
		}
	case "systemctl is-enabled --quiet panel.service":
		if !f.state.enabled {
			return errors.New("disabled")
		}
	case "systemctl is-active --quiet panel.service":
		if !f.state.active {
			return errors.New("inactive")
		}
	}
	return nil
}

// apply folds the mutating commands into hostState, so the next run's probes see
// the host this run built.
func (f *fakeRunner) apply() {
	for _, c := range f.calls {
		joined := strings.Join(append([]string{c.name}, c.args...), " ")
		switch {
		case strings.HasPrefix(joined, "useradd "):
			f.state.userExists = true
		case joined == "systemctl enable --now docker":
			f.state.dockerUp = true
		case joined == "systemctl enable panel.service":
			f.state.enabled = true
		case joined == "systemctl restart panel.service":
			f.state.active = true
		}
	}
}

func (f *fakeRunner) names() []string {
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.String()
	}
	return out
}

// mutating is the commands that change the host: everything that is not a probe.
func (f *fakeRunner) mutating() []string {
	var out []string
	for _, c := range f.names() {
		if isProbe(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func isProbe(command string) bool {
	for _, p := range probePrefixes {
		if strings.HasPrefix(command, p) {
			return true
		}
	}
	return false
}

func (f *fakeRunner) reset() { f.calls = nil }

// fakeOS is osdep.OS over the fake runner.
//
// It emits the argv osdep's Ubuntu implementation emits — the same argument
// vectors its own tests pin in internal/osdep — because the real implementation
// reads /etc/os-release and /etc/passwd, and an install test must not depend on
// the machine it runs on. What this test proves is install's sequence and its
// skip-if-done decisions; the exact argv of EnsureUser and EnsureDocker is
// proven in osdep.
type fakeOS struct {
	runner      *fakeRunner
	detectCalls int
	detectErr   error
	userErr     error
	dockerErr   error
}

var _ osdep.OS = (*fakeOS)(nil)

func (o *fakeOS) DetectDistro() (osdep.Distro, error) {
	o.detectCalls++
	if o.detectErr != nil {
		return "", o.detectErr
	}
	return osdep.DistroUbuntu, nil
}

func (o *fakeOS) InstallPackages(context.Context, []string) error { return nil }

func (o *fakeOS) EnsureUser(ctx context.Context, name string, uid, gid int) error {
	if o.userErr != nil {
		return o.userErr
	}
	if err := o.runner.Run(ctx, "groupadd", "--gid", itoa(gid), name); err != nil {
		return err
	}
	return o.runner.Run(ctx, "useradd",
		"--uid", itoa(uid), "--gid", itoa(gid),
		"--no-create-home", "--home-dir", "/www", "--shell", "/usr/sbin/nologin", name)
}

func (o *fakeOS) EnsureDocker(ctx context.Context) error {
	if o.dockerErr != nil {
		return o.dockerErr
	}
	for _, c := range [][]string{
		{"apt-get", "update"},
		{"apt-get", "install", "-y", "--no-install-recommends", "ca-certificates", "curl", "gnupg"},
		{"install", "-m", "0755", "-d", "/etc/apt/keyrings"},
		{"curl", "--fail", "--silent", "--show-error", "--location", "--output", "/etc/apt/keyrings/docker.asc", "https://download.docker.com/linux/ubuntu/gpg"},
		{"chmod", "a+r", "/etc/apt/keyrings/docker.asc"},
		{"apt-get", "update"},
		{"apt-get", "install", "-y", "--no-install-recommends", "docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"},
		{"systemctl", "enable", "--now", "docker"},
	} {
		if err := o.runner.Run(ctx, c[0], c[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func (o *fakeOS) MaskFirewallServices(context.Context) error { return nil }
func (o *fakeOS) SELinux() osdep.SELinuxStatus               { return osdep.SELinuxStatus{} }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// host is one install run's world: a temporary root, a fake host, and the
// installer wired to both.
type host struct {
	t      *testing.T
	layout Layout
	runner *fakeRunner
	os     *fakeOS
	out    strings.Builder
}

// newHost builds a temp root laid out like the production one, with the unit
// file inside it so no test writes /etc.
func newHost(t *testing.T) *host {
	t.Helper()
	root := t.TempDir()
	return &host{
		t: t,
		layout: Layout{
			Root:     root,
			UnitPath: filepath.Join(root, "etc", "systemd", "system", "panel.service"),
		},
		runner: &fakeRunner{},
	}
}

func (h *host) installer() *Installer {
	if h.os == nil {
		h.os = &fakeOS{runner: h.runner}
	}
	return &Installer{
		Layout:   h.layout,
		OS:       h.os,
		Runner:   h.runner,
		Euid:     func() int { return 0 },
		Out:      &h.out,
		Hostname: "panel.example.test",
	}
}

// install runs one install and folds the commands it issued into the fake host's
// state, which is what makes a second run meaningful.
func (h *host) install() (Report, error) {
	h.t.Helper()
	report, err := h.installer().Install(context.Background())
	h.runner.apply()
	return report, err
}

// TestInstallStepOrder pins the step list: the names, their order, and that
// every step has both a check and a body.
func TestInstallStepOrder(t *testing.T) {
	h := newHost(t)
	s := &runState{Installer: h.installer()}

	want := []string{
		"root",
		"distro",
		"directories",
		"www-user",
		"docker",
		"secret-key",
		"database",
		"admin-password",
		"panel-cert",
		"unit-file",
		"enable-service",
		"start-service",
		"print-url",
	}

	var got []string
	for _, st := range s.steps() {
		got = append(got, st.name)
		if st.done == nil {
			t.Errorf("step %q has no skip-if-done check", st.name)
		}
		if st.run == nil {
			t.Errorf("step %q has no body", st.name)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("step order:\n got %q\nwant %q", got, want)
	}
}

// TestFirstInstallEmitsExpectedArgv is the argv contract for a fresh host: the
// probes, the mutating commands, and the order they arrive in.
func TestFirstInstallEmitsExpectedArgv(t *testing.T) {
	h := newHost(t)
	if _, err := h.install(); err != nil {
		t.Fatalf("install: %v\noutput:\n%s", err, h.out.String())
	}

	want := []string{
		// www-user: probe, then create.
		"getent passwd www",
		"groupadd --gid 1500 www",
		"useradd --uid 1500 --gid 1500 --no-create-home --home-dir /www --shell /usr/sbin/nologin www",
		// docker: probe, then install from Docker's own repository.
		"systemctl is-active --quiet docker",
		"apt-get update",
		"apt-get install -y --no-install-recommends ca-certificates curl gnupg",
		"install -m 0755 -d /etc/apt/keyrings",
		"curl --fail --silent --show-error --location --output /etc/apt/keyrings/docker.asc https://download.docker.com/linux/ubuntu/gpg",
		"chmod a+r /etc/apt/keyrings/docker.asc",
		"apt-get update",
		"apt-get install -y --no-install-recommends docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin",
		"systemctl enable --now docker",
		// unit-file, enable-service, start-service. The is-active probe is
		// deliberately absent: this run rewrote the unit, so it restarts rather
		// than asking whether the old process is up.
		"systemctl daemon-reload",
		"systemctl is-enabled --quiet panel.service",
		"systemctl enable panel.service",
		"systemctl restart panel.service",
	}

	if got := h.runner.names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv:\n got:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestSecondInstallIssuesNoMutatingCommand is the idempotency requirement: a
// second run against the host the first run built must change nothing.
func TestSecondInstallIssuesNoMutatingCommand(t *testing.T) {
	h := newHost(t)
	if _, err := h.install(); err != nil {
		t.Fatalf("first install: %v\noutput:\n%s", err, h.out.String())
	}

	h.runner.reset()
	report, err := h.install()
	if err != nil {
		t.Fatalf("second install: %v\noutput:\n%s", err, h.out.String())
	}

	if got := h.runner.mutating(); len(got) != 0 {
		t.Fatalf("second install issued mutating commands:\n  %s", strings.Join(got, "\n  "))
	}

	// The probes and the checks are the only steps left to run.
	for _, s := range report.Steps {
		switch s.Name {
		case "root", "distro", "print-url":
			if s.Skipped {
				t.Errorf("step %q is always run but was reported skipped", s.Name)
			}
		default:
			if !s.Skipped {
				t.Errorf("step %q ran on a host where it was already done", s.Name)
			}
		}
	}
}

// TestRerunLeavesTheDatabaseAndPasswordAlone is the "never write panel.db twice,
// never reset the password" requirement, checked on the files and the stored
// hash rather than on the commands.
func TestRerunLeavesTheDatabaseAndPasswordAlone(t *testing.T) {
	h := newHost(t)
	if _, err := h.install(); err != nil {
		t.Fatalf("first install: %v", err)
	}

	dbBefore := mustRead(t, h.layout.DBPath())
	keyBefore := mustRead(t, h.layout.SecretKeyPath())
	certBefore := mustRead(t, h.layout.CertPath())
	hashBefore := adminHash(t, h.layout.DBPath())

	if _, err := h.install(); err != nil {
		t.Fatalf("second install: %v", err)
	}

	if got := mustRead(t, h.layout.DBPath()); string(got) != string(dbBefore) {
		t.Error("the second install rewrote panel.db")
	}
	if got := mustRead(t, h.layout.SecretKeyPath()); string(got) != string(keyBefore) {
		t.Error("the second install rewrote secret.key")
	}
	if got := mustRead(t, h.layout.CertPath()); string(got) != string(certBefore) {
		t.Error("the second install regenerated the panel certificate")
	}
	if got := adminHash(t, h.layout.DBPath()); got != hashBefore {
		t.Error("the second install changed the admin password hash")
	}
}

// TestRerunDoesNotPrintThePasswordTwice proves the password is shown once: the
// first run prints it, the second reports the account as unchanged.
func TestRerunDoesNotPrintThePasswordTwice(t *testing.T) {
	h := newHost(t)
	first, err := h.install()
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if first.GeneratedPassword == "" {
		t.Fatal("the first install generated no password")
	}
	if !strings.Contains(h.out.String(), first.GeneratedPassword) {
		t.Fatalf("the first install did not print the password:\n%s", h.out.String())
	}

	h.out.Reset()
	second, err := h.install()
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if second.GeneratedPassword != "" {
		t.Errorf("the second install generated a password %q", second.GeneratedPassword)
	}
	if strings.Contains(h.out.String(), first.GeneratedPassword) {
		t.Errorf("the second install printed the password again:\n%s", h.out.String())
	}
}

// TestRepairFixesALoosenedMode proves a re-run repairs rather than only skips: a
// directory the operator opened up is tightened again.
func TestRepairFixesALoosenedMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are POSIX")
	}

	h := newHost(t)
	if _, err := h.install(); err != nil {
		t.Fatalf("first install: %v", err)
	}

	dataDir := h.layout.DataDir()
	if err := os.Chmod(dataDir, 0o755); err != nil {
		t.Fatalf("loosen %s: %v", dataDir, err)
	}

	h.runner.reset()
	report, err := h.install()
	if err != nil {
		t.Fatalf("repair install: %v", err)
	}

	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("stat %s: %v", dataDir, err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("%s mode = %o after a repair, want 700", dataDir, got)
	}

	if got := h.runner.mutating(); len(got) != 0 {
		t.Fatalf("repair issued mutating commands:\n  %s", strings.Join(got, "\n  "))
	}
	for _, s := range report.Steps {
		if s.Name == "directories" && s.Skipped {
			t.Error("the directories step was skipped on a tree it had to repair")
		}
	}
}

// TestDockerIsSkippedWhenRunning covers the probe: a host already running the
// engine must not have it installed again.
func TestDockerIsSkippedWhenRunning(t *testing.T) {
	h := newHost(t)
	h.runner.state.dockerUp = true

	if _, err := h.install(); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, c := range h.runner.names() {
		if strings.Contains(c, "docker-ce") || strings.HasPrefix(c, "curl ") {
			t.Fatalf("installed Docker on a host already running it:\n  %s",
				strings.Join(h.runner.names(), "\n  "))
		}
	}
}

// TestExistingUserIsNotRecreated covers the getent probe.
func TestExistingUserIsNotRecreated(t *testing.T) {
	h := newHost(t)
	h.runner.state.userExists = true

	if _, err := h.install(); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, c := range h.runner.names() {
		if strings.HasPrefix(c, "useradd ") || strings.HasPrefix(c, "groupadd ") {
			t.Fatalf("recreated an existing user:\n  %s", strings.Join(h.runner.names(), "\n  "))
		}
	}
}

// TestNonRootIsRefused proves the first step refuses before touching anything.
func TestNonRootIsRefused(t *testing.T) {
	h := newHost(t)
	in := h.installer()
	in.Euid = func() int { return 1000 }

	_, err := in.Install(context.Background())
	if err == nil {
		t.Fatal("install as a non-root user returned no error")
	}
	if !strings.Contains(err.Error(), "root") {
		t.Fatalf("error = %v, want it to name the root requirement", err)
	}

	if len(h.runner.calls) != 0 {
		t.Fatalf("ran commands before the root check:\n  %s", strings.Join(h.runner.names(), "\n  "))
	}
	entries, err := os.ReadDir(h.layout.Root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("created %d entries in the root before the root check", len(entries))
	}
}

// TestUnknownDistroStopsBeforeAnyChange covers the distro gate.
func TestUnknownDistroStopsBeforeAnyChange(t *testing.T) {
	h := newHost(t)
	in := h.installer()
	h.os.detectErr = osdep.ErrUnknownDistro

	if _, err := in.Install(context.Background()); err == nil {
		t.Fatal("install on an unsupported distro returned no error")
	}
	if len(h.runner.calls) != 0 {
		t.Fatalf("ran commands on an unsupported distro:\n  %s",
			strings.Join(h.runner.names(), "\n  "))
	}
}

// TestSecretKeyIsRandomAndPrivate pins the key's size and mode.
func TestSecretKeyIsRandomAndPrivate(t *testing.T) {
	h := newHost(t)
	if _, err := h.install(); err != nil {
		t.Fatalf("install: %v", err)
	}

	key := mustRead(t, h.layout.SecretKeyPath())
	if len(key) != secretKeyBytes {
		t.Fatalf("secret.key is %d bytes, want %d", len(key), secretKeyBytes)
	}
	if allZero(key) {
		t.Fatal("secret.key is all zero bytes")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(h.layout.SecretKeyPath())
		if err != nil {
			t.Fatalf("stat secret.key: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("secret.key mode = %o, want 600", got)
		}
	}
}

// TestCertificateIsIssuedForTheHost covers the SANs an operator can actually type.
func TestCertificateIsIssuedForTheHost(t *testing.T) {
	h := newHost(t)
	if _, err := h.install(); err != nil {
		t.Fatalf("install: %v", err)
	}

	pair, err := tls.LoadX509KeyPair(h.layout.CertPath(), h.layout.KeyPath())
	if err != nil {
		t.Fatalf("load certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	names := map[string]bool{}
	for _, dns := range leaf.DNSNames {
		names[dns] = true
	}
	if !names["panel.example.test"] {
		t.Errorf("certificate has no SAN for the hostname: %v", leaf.DNSNames)
	}

	ips := map[string]bool{}
	for _, ip := range leaf.IPAddresses {
		ips[ip.String()] = true
	}
	if !ips["127.0.0.1"] {
		t.Errorf("certificate has no SAN for loopback: %v", leaf.IPAddresses)
	}
}

// TestReportPrintsTheURL covers the last step.
func TestReportPrintsTheURL(t *testing.T) {
	h := newHost(t)
	report, err := h.install()
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if report.URL != "https://0.0.0.0:8443" {
		t.Fatalf("URL = %q, want the registry default bind and port", report.URL)
	}
	if !strings.Contains(h.out.String(), report.URL) {
		t.Errorf("install did not print the URL:\n%s", h.out.String())
	}
}

// TestInstallCreatesTheTreeWithDocumentedModes pins the layout §2 tree.
func TestInstallCreatesTheTreeWithDocumentedModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are POSIX")
	}

	h := newHost(t)
	if _, err := h.install(); err != nil {
		t.Fatalf("install: %v", err)
	}

	for _, d := range h.layout.dirs() {
		info, err := os.Stat(d.path)
		if err != nil {
			t.Errorf("missing %s: %v", d.path, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", d.path)
			continue
		}
		if got := info.Mode().Perm(); got != d.mode {
			t.Errorf("%s mode = %o, want %o (%s)", d.path, got, d.mode, d.why)
		}
	}
}

// TestUnitFileCarriesTheHardeningSet pins plan.md §3.2: every directive present,
// and every filesystem protection absent on purpose.
func TestUnitFileCarriesTheHardeningSet(t *testing.T) {
	unit := UnitFile("/www/panel/bin/panel")

	for _, want := range []string{
		"ExecStart=/www/panel/bin/panel serve",
		"NoNewPrivileges=yes",
		"LockPersonality=yes",
		"RestrictRealtime=yes",
		"ProtectClock=yes",
		"ProtectHostname=yes",
		"SystemCallArchitectures=native",
		"MemoryDenyWriteExecute=yes",
		"Restart=always",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit is missing %q:\n%s", want, unit)
		}
	}

	// Off by design: the file manager has to write anywhere.
	for _, forbidden := range []string{"ProtectSystem", "ProtectHome", "ReadOnlyPaths"} {
		if strings.Contains(unit, forbidden) {
			t.Errorf("unit carries %s, which plan.md §3.2 rules out:\n%s", forbidden, unit)
		}
	}
}

// TestGeneratedPasswordVerifies covers the one thing that would lock the
// operator out of a fresh install: a printed password that does not match the
// stored hash.
func TestGeneratedPasswordVerifies(t *testing.T) {
	h := newHost(t)
	report, err := h.install()
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	ok, err := auth.Verify(adminHash(t, h.layout.DBPath()), report.GeneratedPassword)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Fatal("the printed password does not match the stored hash")
	}
}

// TestOperatorPasswordIsUsedWhenGiven covers the prompt path: a password passed
// in is the one stored.
func TestOperatorPasswordIsUsedWhenGiven(t *testing.T) {
	h := newHost(t)
	in := h.installer()
	in.Password = "a password the operator chose"

	report, err := in.Install(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if report.GeneratedPassword != in.Password {
		t.Fatalf("report password = %q, want the operator's", report.GeneratedPassword)
	}

	ok, err := auth.Verify(adminHash(t, h.layout.DBPath()), in.Password)
	if err != nil || !ok {
		t.Fatalf("Verify(stored hash, operator password) = %v, %v; want true", ok, err)
	}
}

// --- helpers ---------------------------------------------------------------

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func adminHash(t *testing.T, dbPath string) string {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	var hash string
	if err := st.DB().QueryRow(`SELECT password_hash FROM admin WHERE id = 1`).Scan(&hash); err != nil {
		t.Fatalf("read password_hash: %v", err)
	}
	return hash
}
