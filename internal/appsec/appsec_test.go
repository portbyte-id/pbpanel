package appsec

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner records every command and answers by program name and first
// argument, so a test asserts the argument vector a step emits without a host.
type fakeRunner struct {
	// answers maps a program name, and a program with its first argument
	// ("systemctl show", "systemctl is-active"), to what it returns. The more
	// specific key wins.
	answers map[string]string
	// errs maps a key to an error, for the failure paths.
	errs map[string]error
	// effects run after a matching command, so a fake can change the fake
	// filesystem the way its real counterpart does.
	effects map[string]func()
	// calls is every command in order.
	calls []Command
}

// key is the lookup key for a command: the program alone, or program and first
// argument when that is more specific.
func key(c Command) (specific, general string) {
	general = c.Name
	if len(c.Args) > 0 {
		specific = c.Name + " " + c.Args[0]
	}
	return specific, general
}

func (f *fakeRunner) Run(_ context.Context, c Command) ([]byte, error) {
	f.calls = append(f.calls, c)
	specific, general := key(c)
	if eff, ok := f.effects[specific]; ok {
		eff()
	} else if eff, ok := f.effects[general]; ok {
		eff()
	}
	if err, ok := f.errs[specific]; ok {
		return []byte(f.answers[specific]), err
	}
	if err, ok := f.errs[general]; ok {
		return []byte(f.answers[general]), err
	}
	if a, ok := f.answers[specific]; ok {
		return []byte(a), nil
	}
	return []byte(f.answers[general]), nil
}

// argv returns, for every command run, its program and arguments joined.
func (f *fakeRunner) argv() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, strings.TrimSpace(c.Name+" "+strings.Join(c.Args, " ")))
	}
	return out
}

// fakeFS is the in-memory filesystem the tests run the manager over, so nothing
// needs root or a real /etc.
type fakeFS struct {
	files map[string][]byte
	dirs  map[string]bool
}

func newFakeFS() *fakeFS {
	return &fakeFS{files: map[string][]byte{}, dirs: map[string]bool{}}
}

type pathError struct {
	op, path string
	err      error
}

func (e *pathError) Error() string { return e.op + " " + e.path + ": " + e.err.Error() }
func (e *pathError) Unwrap() error { return e.err }

// fileInfo is a regular file's os.FileInfo, with no ModTime to speak of: nothing
// in this package reads one.
type fileInfo struct {
	name string
	size int64
	mode os.FileMode
}

func (f fileInfo) Name() string       { return f.name }
func (f fileInfo) Size() int64        { return f.size }
func (f fileInfo) Mode() os.FileMode  { return f.mode }
func (f fileInfo) ModTime() time.Time { return time.Time{} }
func (f fileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fileInfo) Sys() any           { return nil }

func (f *fakeFS) read(name string) ([]byte, error) {
	if b, ok := f.files[name]; ok {
		return b, nil
	}
	return nil, &pathError{op: "open", path: name, err: os.ErrNotExist}
}

func (f *fakeFS) stat(name string) (os.FileInfo, error) {
	if b, ok := f.files[name]; ok {
		return fileInfo{name: filepath.Base(name), size: int64(len(b)), mode: 0o644}, nil
	}
	if f.dirs[name] {
		return fileInfo{name: filepath.Base(name), mode: os.ModeDir | 0o755}, nil
	}
	return nil, &pathError{op: "stat", path: name, err: os.ErrNotExist}
}

type harness struct {
	m   *Manager
	run *fakeRunner
	fs  *fakeFS
}

// newHarness builds a manager pinned to the sandbox's codename, over the fake
// filesystem, with the agent present or absent as installed says.
func newHarness(t *testing.T, installed bool) *harness {
	t.Helper()
	fs := newFakeFS()
	fs.dirs[FilesystemRoot] = true
	fs.dirs[filepath.Join(FilesystemRoot, "conf")] = true
	fs.dirs[filepath.Join(FilesystemRoot, "scripts")] = true
	if installed {
		fs.files[filepath.Join(FilesystemRoot, "watchdog", "cp-nano-watchdog")] = []byte("#!/bin/sh\n")
	}
	run := &fakeRunner{answers: map[string]string{}, errs: map[string]error{}, effects: map[string]func(){}}
	m := New(Options{
		Root: FilesystemRoot, Codename: "resolute",
		Run:           run,
		ReadFile:      fs.read,
		Stat:          fs.stat,
		WriteFile:     func(name string, data []byte, _ os.FileMode) error { fs.files[name] = data; return nil },
		MkdirAll:      func(name string, _ os.FileMode) error { fs.dirs[name] = true; return nil },
		MkdirTemp:     func(string, string) (string, error) { return "/tmp/panel-appsec-test", nil },
		RemoveAll:     func(string) error { return nil },
		ModulePresent: func(context.Context) (bool, error) { return true, nil },
		Fetch:         func(_ context.Context, _ Release, dest string) error { fs.files[dest] = []byte("archive"); return nil },
	})
	// tar lays down the one directory the vendor's archive holds; a test that wants
	// the install to reach the installers needs the script the manager looks for.
	run.effects["tar"] = func() {
		fs.dirs["/tmp/panel-appsec-test/openappsec"] = true
		fs.files["/tmp/panel-appsec-test/openappsec/install-cp-nano-agent.sh"] = []byte("#!/bin/sh\n")
	}
	return &harness{m: m, run: run, fs: fs}
}

// active makes the fake systemd report the agent's unit running and the vendor's
// report healthy. nginx is answered as not running, so an install skips the reload
// unless a test says otherwise.
func (h *harness) active() {
	h.run.answers["systemctl show"] = "LoadState=loaded\nActiveState=active\n"
	h.run.answers["systemctl is-active"] = "inactive\n"
	h.run.answers[h.m.Ctl()] = "---- open-appsec Nano Agent ----\nVersion: 1.1.37-open-source\n" +
		"Status: Running\nPolicy load status: Success\n"
}

func TestWarningNamesCheckPointAndTheConnection(t *testing.T) {
	for _, want := range []string{"Check Point", "continuous", "tracking", "no supported way"} {
		if !strings.Contains(Warning, want) {
			t.Errorf("Warning does not say %q: %q", want, Warning)
		}
	}
	// The plan is explicit that it must not be softened into optional telemetry.
	for _, banned := range []string{"optional telemetry", "telemetry is optional", "may be disabled", "privacy"} {
		if strings.Contains(strings.ToLower(Warning), banned) {
			t.Errorf("Warning reads as optional or as a privacy switch (%q): %q", banned, Warning)
		}
	}
}

func TestReleaseForRefusesAnUnpinnedCodename(t *testing.T) {
	if _, err := ReleaseFor("jammy"); err == nil {
		t.Fatal("jammy has no pinned archive and must be refused")
	}
	rel, err := ReleaseFor("resolute")
	if err != nil {
		t.Fatalf("resolute is pinned: %v", err)
	}
	if len(rel.SHA256) != 64 {
		t.Errorf("SHA256 = %q, want 64 hex characters", rel.SHA256)
	}
	if !strings.HasPrefix(rel.URL, "https://") {
		t.Errorf("URL = %q, want https", rel.URL)
	}
	// The archive is per codename, so two codenames may not share a URL or a pin.
	noble, err := ReleaseFor("noble")
	if err != nil {
		t.Fatal(err)
	}
	if noble.URL == rel.URL || noble.SHA256 == rel.SHA256 {
		t.Errorf("noble and resolute share a URL or a digest: %+v vs %+v", noble, rel)
	}
}

func TestSupportedCodenamesIsSorted(t *testing.T) {
	got := SupportedCodenames()
	if len(got) < 2 {
		t.Fatalf("expected at least the two pinned codenames, got %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Errorf("codenames are not sorted: %v", got)
		}
	}
}

func TestParseCodename(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"PRETTY_NAME=\"Ubuntu 26.04 LTS\"\nVERSION_CODENAME=resolute\n", "resolute"},
		{"UBUNTU_CODENAME=\"noble\"\n", "noble"},
	} {
		got, err := parseCodename(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("parseCodename(%q) = %q, %v", tc.in, got, err)
		}
	}
	if _, err := parseCodename("ID=ubuntu\n"); err == nil {
		t.Error("a file with no codename must be an error, not an empty codename")
	}
}

func TestParseUnitStatus(t *testing.T) {
	if loaded, state := ParseUnitStatus("LoadState=loaded\nActiveState=inactive\n"); !loaded || state != "inactive" {
		t.Errorf("loaded=%v state=%q", loaded, state)
	}
	if loaded, state := ParseUnitStatus("LoadState=not-found\nActiveState=\n"); loaded || state != "" {
		t.Errorf("loaded=%v state=%q, want not loaded", loaded, state)
	}
	// A property systemd adds later is skipped, not an error.
	if loaded, state := ParseUnitStatus("Future=thing\nLoadState=loaded\nActiveState=active\n"); !loaded || state != "active" {
		t.Errorf("loaded=%v state=%q", loaded, state)
	}
}

func TestParseCtlVersionAndPolicyLoad(t *testing.T) {
	const report = `---- open-appsec Nano Agent ----
Version: 1.1.37-open-source
Status: Running
Policy files:
    /etc/cp/conf/local_policy.yaml
Policy load status: Success
`
	if got := ParseCtlVersion(report); got != "1.1.37-open-source" {
		t.Errorf("version = %q", got)
	}
	if got := ParsePolicyLoad(report); got != "Success" {
		t.Errorf("policy load = %q", got)
	}
	if got := ParseCtlVersion("nothing here\n"); got != "" {
		t.Errorf("version of a report without one = %q, want empty", got)
	}
}

func TestDefaultPolicyInspectsNothing(t *testing.T) {
	p := string(DefaultPolicy())
	for _, want := range []string{"apiVersion: v1beta2", "mode: inactive", "specificRules: []"} {
		if !strings.Contains(p, want) {
			t.Errorf("policy does not say %q: %s", want, p)
		}
	}
	if !PolicyIsDefault(DefaultPolicy()) {
		t.Error("DefaultPolicy must be recognised as itself")
	}
	if PolicyIsDefault([]byte("apiVersion: v1beta2\npolicies:\n  default:\n    mode: detect-learn\n")) {
		t.Error("an inspecting policy must not read as the default")
	}
}

func TestStatusReportsInstalledAndActive(t *testing.T) {
	h := newHarness(t, true)
	h.active()
	st, err := h.m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || !st.Active || st.State != "active" {
		t.Fatalf("status = %+v", st)
	}
	if st.Version != "1.1.37-open-source" || st.PolicyLoad != "Success" {
		t.Errorf("version/policy = %q/%q", st.Version, st.PolicyLoad)
	}
	if st.Reason != "" || !st.Ready() {
		t.Errorf("a running agent has no reason and is ready: %+v", st)
	}
}

func TestStatusOfAnAbsentAgentAsksNothing(t *testing.T) {
	h := newHarness(t, false)
	st, err := h.m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Installed || st.Active || st.Ready() {
		t.Fatalf("status = %+v, want not installed", st)
	}
	if !strings.Contains(st.Reason, "not installed") {
		t.Errorf("reason = %q", st.Reason)
	}
	if len(h.run.calls) != 0 {
		t.Errorf("an absent agent must not be asked anything, ran %v", h.run.argv())
	}
}

// A stopped agent is installed but not inspecting, which is what the availability
// must report: every file is in place and the engine still does nothing.
func TestStatusOfAStoppedAgent(t *testing.T) {
	h := newHarness(t, true)
	h.run.answers["systemctl show"] = "LoadState=loaded\nActiveState=inactive\n"
	st, err := h.m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || st.Active || st.Ready() {
		t.Fatalf("status = %+v, want installed and not active", st)
	}
	if !strings.Contains(st.Reason, "inactive") {
		t.Errorf("reason = %q", st.Reason)
	}
}

func TestAvailableNeedsTheModule(t *testing.T) {
	h := newHarness(t, true)
	h.active()
	m := New(Options{
		Root: FilesystemRoot, Codename: "resolute", Run: h.run, Stat: h.fs.stat, ReadFile: h.fs.read,
		ModulePresent: func(context.Context) (bool, error) { return false, nil },
	})
	if ok, reason := m.Available(context.Background()); ok || !strings.Contains(reason, "attachment") {
		t.Fatalf("no module: available=%v reason=%q", ok, reason)
	}
}

func TestAvailableNeedsTheAgent(t *testing.T) {
	h := newHarness(t, false)
	ok, reason := h.m.Available(context.Background())
	if ok || !strings.Contains(reason, "not installed") {
		t.Fatalf("no agent: available=%v reason=%q", ok, reason)
	}
}

func TestAvailableWithModuleAndAgent(t *testing.T) {
	h := newHarness(t, true)
	h.active()
	if ok, reason := h.m.Available(context.Background()); !ok {
		t.Fatalf("available=%v reason=%q", ok, reason)
	}
}

func TestAvailabilityCarriesTheWarningAndPinnedVersion(t *testing.T) {
	h := newHarness(t, true)
	h.active()
	a := h.m.Availability(context.Background())
	if a.Warning != Warning || a.Acknowledgement != Acknowledgement {
		t.Error("the availability must carry the warning and acknowledgement verbatim")
	}
	if a.PinnedVersion != "1.1.37" {
		t.Errorf("pinned version = %q", a.PinnedVersion)
	}
	if !a.Available || !a.Installed || !a.AgentActive {
		t.Errorf("availability = %+v", a)
	}
}

func TestInstallRefusesWithoutTheAcknowledgement(t *testing.T) {
	for _, ack := range []string{"", "yes", "true", "open-appsec"} {
		h := newHarness(t, false)
		err := h.m.Install(context.Background(), InstallOptions{Acknowledge: ack})
		if !errors.Is(err, ErrNotAcknowledged) {
			t.Errorf("ack %q: err = %v, want ErrNotAcknowledged", ack, err)
		}
		if len(h.run.calls) != 0 {
			t.Errorf("ack %q: install ran %v before refusing", ack, h.run.argv())
		}
	}
}

// TestInstallRunsTheStepsInOrder pins the two orders that are not incidental: the
// three vendor installers run in the documented sequence, and the nginx reload
// comes after the agent is up and the policy is applied (§0.4).
func TestInstallRunsTheStepsInOrder(t *testing.T) {
	h := newHarness(t, false)
	h.run.answers["systemctl show"] = "LoadState=loaded\nActiveState=active\n"
	h.run.answers["systemctl is-active"] = "active\n"
	h.run.answers[h.m.Ctl()] = "Applying new policy.\nNew policy applied.\n"
	// The install leaves the watchdog present, which is what makes Status report the
	// agent installed afterwards.
	h.fs.files[filepath.Join(FilesystemRoot, "watchdog", "cp-nano-watchdog")] = []byte("x")
	// The agent's IPC socket is up, which the install waits for before reloading nginx.
	h.m.stat = func(name string) (os.FileInfo, error) {
		if name == agentIPCSocket {
			return socketInfo{}, nil
		}
		return h.fs.stat(name)
	}

	var lines []string
	err := h.m.Install(context.Background(), InstallOptions{
		Acknowledge: Acknowledgement,
		Progress:    func(l string) { lines = append(lines, l) },
	})
	if err != nil {
		t.Fatalf("install: %v (%v)", err, h.run.argv())
	}

	got := h.run.argv()
	var installers []string
	for _, c := range got {
		if strings.Contains(c, "install-cp-nano-") {
			installers = append(installers, filepath.Base(strings.Fields(c)[0]))
		}
	}
	want := []string{
		"install-cp-nano-agent.sh",
		"install-cp-nano-service-http-transaction-handler.sh",
		"install-cp-nano-attachment-registration-manager.sh",
	}
	if strings.Join(installers, ",") != strings.Join(want, ",") {
		t.Fatalf("installers = %v, want %v (all commands: %v)", installers, want, got)
	}

	unpack := indexOf(got, "tar -xzf")
	apply := indexOf(got, "--apply-policy")
	reload := indexOf(got, "systemctl reload")
	if unpack < 0 || apply < 0 || reload < 0 {
		t.Fatalf("a command is missing: unpack %d, apply %d, reload %d in %v", unpack, apply, reload, got)
	}
	if unpack >= indexOf(got, "install-cp-nano-agent.sh") || apply >= reload {
		t.Errorf("order wrong in %v", got)
	}
	if _, ok := h.fs.files[h.m.PolicyPath()]; !ok {
		t.Error("the install wrote no policy")
	}
	if len(lines) == 0 {
		t.Error("the install reported no progress")
	}
}

// indexOf is the position of the first command whose text contains substr.
func indexOf(cmds []string, substr string) int {
	for i, c := range cmds {
		if strings.Contains(c, substr) {
			return i
		}
	}
	return -1
}

// The policy is applied before the reload, so the agent never has to score a
// request under the vendor's stock all-detect policy.
func TestApplyPolicyFollowsThePolicyWrite(t *testing.T) {
	h := newHarness(t, false)
	h.run.answers["systemctl show"] = "LoadState=loaded\nActiveState=active\n"
	h.run.answers[h.m.Ctl()] = "New policy applied.\n"
	h.fs.files[filepath.Join(FilesystemRoot, "watchdog", "cp-nano-watchdog")] = []byte("x")

	if err := h.m.Install(context.Background(), InstallOptions{Acknowledge: Acknowledgement}); err != nil {
		t.Fatal(err)
	}
	apply := -1
	for i, c := range h.run.calls {
		if len(c.Args) > 0 && c.Args[0] == "--apply-policy" {
			apply = i
			if c.Args[1] != h.m.PolicyPath() {
				t.Errorf("applied %q, want the manager's policy %q", c.Args[1], h.m.PolicyPath())
			}
		}
	}
	if apply < 0 {
		t.Fatal("no --apply-policy ran")
	}
}

// The vendor's `--apply-policy` ends with `exit 1` after printing success in
// 1.1.37, so the manager has to read the text and not the status (§6.1).
func TestInstallAcceptsApplyPolicyExitCodeOne(t *testing.T) {
	h := newHarness(t, false)
	h.run.answers["systemctl show"] = "LoadState=loaded\nActiveState=active\n"
	h.run.answers[h.m.Ctl()] = "Applying new policy.\nNew policy applied.\n"
	h.run.errs[h.m.Ctl()] = errors.New("exit status 1")
	h.fs.files[filepath.Join(FilesystemRoot, "watchdog", "cp-nano-watchdog")] = []byte("x")

	if err := h.m.Install(context.Background(), InstallOptions{Acknowledge: Acknowledgement}); err != nil {
		t.Fatalf("a successful apply that exits 1 must not fail the install: %v", err)
	}
}

// A policy the agent did not accept is a failure, even with a zero exit code.
func TestInstallFailsWhenThePolicyIsNotApplied(t *testing.T) {
	h := newHarness(t, false)
	h.run.answers["systemctl show"] = "LoadState=loaded\nActiveState=active\n"
	h.run.answers[h.m.Ctl()] = "Applying new policy.\nPolicy didn't change please verify that you have a legal new policy\n"
	h.fs.files[filepath.Join(FilesystemRoot, "watchdog", "cp-nano-watchdog")] = []byte("x")

	err := h.m.Install(context.Background(), InstallOptions{Acknowledge: Acknowledgement})
	if !errors.Is(err, ErrPolicyRejected) {
		t.Fatalf("err = %v, want ErrPolicyRejected", err)
	}
}

// An agent that did not come up must fail the install: a job that reported success
// for a stopped agent would leave every site uninspected and the Store saying it
// worked.
func TestInstallFailsWhenTheAgentStaysDown(t *testing.T) {
	h := newHarness(t, false)
	h.run.answers["systemctl show"] = "LoadState=loaded\nActiveState=failed\n"
	h.fs.files[filepath.Join(FilesystemRoot, "watchdog", "cp-nano-watchdog")] = []byte("x")

	err := h.m.Install(context.Background(), InstallOptions{Acknowledge: Acknowledgement})
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("err = %v, want the unit's failed state", err)
	}
}

func TestUninstallRefusesWhileASiteUsesTheEngine(t *testing.T) {
	h := newHarness(t, true)
	h.active()
	err := h.m.Uninstall(context.Background(), func(context.Context) (bool, error) { return true, nil })
	if !errors.Is(err, ErrInUse) {
		t.Fatalf("err = %v, want ErrInUse", err)
	}
	for _, c := range h.run.calls {
		if len(c.Args) > 0 && c.Args[0] == "--uninstall" {
			t.Fatal("the uninstaller ran while a site used the engine")
		}
	}
}

// A check that cannot answer must stop the uninstall: "probably unused" is not good
// enough to remove an engine a site may depend on.
func TestUninstallRefusesWhenTheInUseCheckFails(t *testing.T) {
	h := newHarness(t, true)
	h.active()
	boom := errors.New("database is busy")
	if err := h.m.Uninstall(context.Background(), func(context.Context) (bool, error) { return false, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the check's error", err)
	}
}

func TestUninstallAnswersThePromptOverStdin(t *testing.T) {
	h := newHarness(t, true)
	h.active()
	h.run.answers[h.m.Ctl()] = "open-appsec Nano Agent successfully uninstalled\n"
	// The vendor's uninstaller removes the payload, which is what makes the agent
	// read as gone afterwards.
	h.run.effects[h.m.Ctl()] = func() {
		delete(h.fs.files, filepath.Join(FilesystemRoot, "watchdog", "cp-nano-watchdog"))
	}

	if err := h.m.Uninstall(context.Background(), func(context.Context) (bool, error) { return false, nil }); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	var uninstall *Command
	for i := range h.run.calls {
		if h.run.calls[i].Name == h.m.Ctl() {
			uninstall = &h.run.calls[i]
		}
	}
	if uninstall == nil {
		t.Fatal("the uninstaller did not run")
	}
	if uninstall.Stdin != "Y\n" {
		t.Errorf("stdin = %q, want the confirmation", uninstall.Stdin)
	}
}

func TestUninstallOfAnAbsentAgent(t *testing.T) {
	h := newHarness(t, false)
	if err := h.m.Uninstall(context.Background(), nil); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v, want ErrNotInstalled", err)
	}
}

// The uninstall must not trust the script's exit status alone: an agent still on
// disk after the run is a failure the caller has to see.
func TestUninstallFailsWhenTheAgentRemains(t *testing.T) {
	h := newHarness(t, true)
	h.active()
	if err := h.m.Uninstall(context.Background(), nil); err == nil {
		t.Fatal("an uninstall that left the agent in place must fail")
	}
}

func TestArchitectureIsX86_64(t *testing.T) {
	if a, err := Architecture(); err != nil || a != "x86_64" {
		t.Fatalf("Architecture = %q, %v", a, err)
	}
}

// TestDefaultFetchRefusesAWrongDigest exercises the real download path against a
// local server, so the digest check and the leave-nothing-behind rule run for real
// and no network is needed.
func TestDefaultFetchRefusesAWrongDigest(t *testing.T) {
	body := strings.Repeat("agent", 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	// DefaultFetch refuses a non-https URL before dialling, so the local server is
	// named with the scheme the check accepts only through the test seam: the check
	// itself is exercised below, and the transport here is what verifies the body.
	dest := filepath.Join(t.TempDir(), "agent.tar.gz")
	err := fetchFromForTest(context.Background(), srv.URL, strings.Repeat("0", 64), dest)
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("err = %v, want the digest mismatch", err)
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("a refused download must leave nothing at dest")
	}
}

func TestDefaultFetchAcceptsThePinnedDigest(t *testing.T) {
	body := strings.Repeat("agent", 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "agent.tar.gz")
	if err := fetchFromForTest(context.Background(), srv.URL, sha256Hex(body), dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != body {
		t.Fatalf("dest = %q, %v", got, err)
	}
	if info, err := os.Stat(dest); err == nil && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestDefaultFetchRefusesPlainHTTP(t *testing.T) {
	err := DefaultFetch(context.Background(), Release{URL: "http://example.test/a.tar.gz", SHA256: strings.Repeat("a", 64)},
		filepath.Join(t.TempDir(), "a"))
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("err = %v, want a refusal of http", err)
	}
}

func TestDefaultFetchRefusesAHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	err := fetchFromForTest(context.Background(), srv.URL, strings.Repeat("a", 64), filepath.Join(t.TempDir(), "a"))
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want HTTP 404", err)
	}
}
