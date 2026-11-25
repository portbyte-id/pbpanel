// Package appsec installs, removes and reports the open-appsec agent: Check
// Point's machine-learning inspection engine, the second engine a site's
// firewall can run.
//
// The agent is not a package in the distribution's repository. It is a set of
// tarballs fetched from downloads.openappsec.io and installed by the vendor's own
// scripts, and the download URL is not versioned: any path answers 200 for
// whatever release is current. So the thing that pins a release is the SHA-256 of
// the archive, stored here, never a version in a URL. Changing the pinned
// release is a deliberate panel change.
//
// Nothing in this package decides whether a site uses the engine; that is the
// firewall's model. It owns the agent's lifecycle and the facts the panel reports
// about it, and it writes the one policy file an install with no sites leaves
// behind: a policy that inspects nothing.
//
// Every command this package runs is an argument vector. The vendor's installers
// are self-extracting shell scripts, so the kernel starts them through their own
// shebang; the panel never builds a shell line. The uninstaller's confirmation
// prompt is answered over stdin.
package appsec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// The installed agent's fixed locations, all as measured on the Ubuntu sandbox
// (docs/design/open-appsec-plan.md §6). They are constants rather than settings:
// the vendor's scripts write and read here, and a panel that pointed anywhere
// else would be describing an install the agent does not have.
const (
	// FilesystemRoot is where the agent keeps everything: binaries, configuration
	// and its scripts.
	FilesystemRoot = "/etc/cp"

	// UnitName is the one systemd unit the agent runs. The four real services are
	// children of the watchdog this unit starts.
	UnitName = "nano_agent.service"

	// CtlPath is the vendor's control script, the only supported way to apply a
	// policy, read the status or uninstall.
	CtlPath = "/etc/cp/scripts/open-appsec-ctl.sh"

	// PolicyPath is the declarative policy the agent reads: schema v1beta2.
	PolicyPath = "/etc/cp/conf/local_policy.yaml"

	// NginxUnitName is the unit that has to reload after the agent comes up. The
	// attachment registers against the agent's /dev/shm sockets at nginx start or
	// reload only, so an agent installed under a running nginx enforces nothing
	// until nginx is reloaded (§0.4).
	NginxUnitName = "panel-nginx.service"
)

// Warning is the tracking statement the operator has to acknowledge before the
// agent can be installed, and the one the Store card shows before that.
//
// It exists because of the D5 decision (docs/design/open-appsec-plan.md §6.1):
// the operator accepted open-appsec knowing it phones home, and that acceptance
// is honest only if the panel says so at the point of install. In local-policy
// mode the agent still holds TLS connections to Check Point's cloud
// (inext-agents.cloud.ngen.checkpoint.com); no supported flag turns them off, and
// blocking them degrades enforcement — §0.7 and §6.1 measured an egress block
// making `--apply-policy` fail and the agent stop inspecting. This is not
// optional telemetry and must never be described as such.
//
// It is one constant, exposed to the interface through the API, so the Store card
// and the confirmation show exactly this text rather than a paraphrase that
// drifts from what the operator agreed to.
const Warning = "The open-appsec agent keeps a continuous TLS connection to Check Point's cloud " +
	"(inext-agents.cloud.ngen.checkpoint.com). There is no supported way to turn that connection off: " +
	"--offline_mode does not stop it, and blocking it degrades enforcement, because the agent stops " +
	"inspecting while the connection is unavailable. Running this engine can subject this host and its " +
	"traffic to Check Point's tracking. Install the agent only if you accept that."

// Acknowledgement is the exact value an install request must carry to prove the
// operator read Warning. The API refuses an install without it, and so does the
// manager, so a caller that starts the job directly cannot skip it either.
const Acknowledgement = "open-appsec-cloud-connection-accepted"

// Pinned releases, one per Ubuntu codename the vendor publishes: the archive is
// built per codename and the digests differ, so noble's archive is not
// resolute's. A codename the vendor does not publish for is refused by
// ReleaseFor rather than served another codename's archive.
//
// The version is a label only. Every release the vendor has published answers the
// same URL — 1.1.36, 1.1.37 and 1.1.38 were all 200 for the same bytes — so the
// SHA-256 is the pin, and a download that does not match it is refused instead of
// installed.
var releases = map[string]Release{
	"noble": {
		Version: "1.1.37",
		URL:     "https://downloads.openappsec.io/packages/agent/x86_64/ubuntu/noble/openappsec-noble.tar.gz",
		SHA256:  "2d85d6254330b0449bc9529684c7f4858a00e37a8ae23f38ea4e7233211cc749",
	},
	"resolute": {
		Version: "1.1.37",
		URL:     "https://downloads.openappsec.io/packages/agent/x86_64/ubuntu/resolute/openappsec-resolute.tar.gz",
		SHA256:  "ea86695100582c1ca0b538eed7ba45a109929195be588a468d2f92bc94d263ba",
	},
}

// Release is one pinned agent archive: where it comes from and the SHA-256 it
// must have.
type Release struct {
	Version string
	URL     string
	SHA256  string
}

// ReleaseFor is the pinned release for an Ubuntu codename.
func ReleaseFor(codename string) (Release, error) {
	rel, ok := releases[codename]
	if !ok {
		return Release{}, fmt.Errorf("appsec: open-appsec publishes no agent for Ubuntu %q; the panel pins %s",
			codename, strings.Join(SupportedCodenames(), ", "))
	}
	return rel, nil
}

// SupportedCodenames lists, sorted, the Ubuntu codenames with a pinned agent.
func SupportedCodenames() []string {
	out := make([]string, 0, len(releases))
	for c := range releases {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Architecture is the agent archive's architecture directory. The vendor
// publishes only x86_64 today; anything else is refused rather than guessed at.
func Architecture() (string, error) {
	switch goarch {
	case "amd64":
		return "x86_64", nil
	default:
		return "", fmt.Errorf("appsec: open-appsec publishes no agent for %s; only amd64 is supported", goarch)
	}
}

// goarch is the host architecture; a variable so tests can run the same
// assertions on an arm64 runner.
var goarch = runtime.GOARCH

var (
	// ErrNotInstalled is returned when an operation needs an agent that is not there.
	ErrNotInstalled = errors.New("appsec: the open-appsec agent is not installed")
	// ErrNotAcknowledged is returned when an install request does not carry the
	// acknowledgement of Warning.
	ErrNotAcknowledged = errors.New("appsec: the tracking warning has not been acknowledged; read it and send it back to install")
	// ErrInUse is returned by Uninstall while a site still runs the engine.
	ErrInUse = errors.New("appsec: a site still uses the open-appsec engine; switch those sites to ModSecurity or off before uninstalling")
	// ErrPolicyRejected is returned when the agent does not report the new policy
	// as loaded.
	ErrPolicyRejected = errors.New("appsec: the agent did not load the new policy")
)

// Status is what the panel knows about the agent on this host.
type Status struct {
	// Installed says the agent's payload is on disk.
	Installed bool `json:"installed"`
	// Version is the installed release, as the vendor's own script reports it, or
	// "" when nothing is installed or the version could not be read.
	Version string `json:"version"`
	// Active says systemd reports nano_agent.service running. It is read from the
	// unit, not inferred from the files: an installed agent whose unit is stopped
	// inspects nothing.
	Active bool `json:"active"`
	// State is systemd's ActiveState verbatim ("active", "failed", "inactive"),
	// or "" when the unit could not be read.
	State string `json:"state"`
	// PolicyLoad is the agent's own verdict on the last policy it loaded
	// ("Success" or "Error"), or "" when it cannot be read.
	PolicyLoad string `json:"policy_load"`
	// Reason says, in one sentence, what is missing when the agent is not up.
	// Empty when it is installed and running.
	Reason string `json:"reason"`
}

// Ready reports whether the agent is installed and running: the condition a site
// needs before it may use the engine.
func (s Status) Ready() bool { return s.Installed && s.Active }

// Availability is what the Store card and the firewall pages read: whether the
// engine can be used, why not when it cannot, and the warning the operator has to
// acknowledge before installing.
//
// It is this package's own type rather than an extension of the firewall's
// Availability, which stays a single boolean and reason: the engine's
// availability is one input to that, and the Store needs more than a boolean.
type Availability struct {
	// Available means the serving nginx build has the attachment and the agent is
	// installed and active: a site may now choose the engine.
	Available bool `json:"available"`
	// Reason says what is missing when Available is false.
	Reason string `json:"reason"`
	// Installed, AgentActive, AgentVersion and PolicyLoad are the agent's own
	// state, so the Store can show it whether or not the module is present.
	Installed    bool   `json:"installed"`
	AgentActive  bool   `json:"agent_active"`
	AgentVersion string `json:"agent_version"`
	PolicyLoad   string `json:"policy_load"`
	// PinnedVersion is the release this panel installs.
	PinnedVersion string `json:"pinned_version"`
	// Warning is the tracking statement, verbatim, for the card and the
	// confirmation to render.
	Warning string `json:"warning"`
	// Acknowledgement is the value an install request must send back to prove the
	// operator read Warning.
	Acknowledgement string `json:"acknowledgement"`
}

// Command is one external command: the program, its argument vector, and the two
// things a vendor script needs beyond them.
type Command struct {
	Name string
	Args []string
	// Dir is the working directory, "" for the daemon's own.
	Dir string
	// Stdin answers a script's prompt. It is how the vendor's uninstaller's
	// confirmation is answered without a pipe or a terminal.
	Stdin string
}

// Runner runs one command and returns its combined output.
type Runner interface {
	Run(ctx context.Context, c Command) ([]byte, error)
}

// FetchFunc downloads rel to dest and returns an error unless its SHA-256 is the
// pinned one.
type FetchFunc func(ctx context.Context, rel Release, dest string) error

// UnpackCommand unpacks the agent archive into dir with tar: a program and an
// argument vector, not a shell.
func UnpackCommand(archive, dir string) Command {
	return Command{Name: "tar", Args: []string{"-xzf", archive, "-C", dir}}
}

// installScripts are the vendor's three installers, in the order they must run.
// The attachment registration manager is required: the nginx attachment cannot
// register against the agent without it. There is no separate
// http-transaction-handler package; that name is only the binary the second
// script installs (§6.1).
var installScripts = []string{
	"install-cp-nano-agent.sh --install --hybrid_mode",
	"install-cp-nano-service-http-transaction-handler.sh --install",
	"install-cp-nano-attachment-registration-manager.sh --install",
}

// InstallCommands are the vendor's three installers, each run from dir, the
// directory the archive was unpacked into. `--hybrid_mode` is the local-policy
// mode — no token, no SaaS registration — and it is not a privacy switch: the
// agent still connects to the cloud, which is what Warning says.
//
// They are executable files with their own `#!/bin/sh` first line, so they run as
// programs. The panel does not build a shell command line for them.
func InstallCommands(dir string) []Command {
	out := make([]Command, 0, len(installScripts))
	for _, s := range installScripts {
		name, args, _ := strings.Cut(s, " ")
		out = append(out, Command{Name: filepath.Join(dir, name), Args: strings.Fields(args), Dir: dir})
	}
	return out
}

// UninstallCommand is the vendor's uninstaller. It asks for confirmation on
// stdin, so the answer is supplied rather than left to a terminal that is not
// there.
func UninstallCommand() Command {
	return Command{Name: CtlPath, Args: []string{"--uninstall"}, Stdin: "Y\n"}
}

// ParseUnitStatus reads the output of `systemctl show -p LoadState -p ActiveState`.
// An unreadable line is skipped rather than failing the whole answer, because
// systemd's output gains properties between releases.
func ParseUnitStatus(out string) (loaded bool, state string) {
	for line := range strings.SplitSeq(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "LoadState":
			loaded = v == "loaded"
		case "ActiveState":
			state = v
		}
	}
	return loaded, state
}

// ParseCtlVersion reads the agent's version out of `open-appsec-ctl --status`.
// The report's first service block carries a line `Version: 1.1.37-open-source`,
// and the first such line is the agent's.
func ParseCtlVersion(out string) string {
	for line := range strings.SplitSeq(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Version:")
		if !ok {
			continue
		}
		return strings.TrimSpace(rest)
	}
	return ""
}

// ParsePolicyLoad reads the agent's verdict on the policy it last loaded out of
// `open-appsec-ctl --status`.
func ParsePolicyLoad(out string) string {
	for line := range strings.SplitSeq(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Policy load status:")
		if !ok {
			continue
		}
		return strings.TrimSpace(rest)
	}
	return ""
}

// DefaultPolicy is the policy an install with no sites leaves behind: a default
// practice that inspects nothing, and no specific rules.
//
// An all-`inactive` default is the belt whose braces are the per-site directive: a
// host that runs the agent for one engine site must not score every other host.
// The vendor's stock policy would inspect everything at `detect-learn`, which is
// the opposite of what an install with nothing enabled should do.
//
// It is a constant rather than a render from the model because no engine site can
// exist before the rendering step; the renderer that reads specificRules arrives
// with it.
func DefaultPolicy() []byte {
	return []byte(`# Rendered by Server Panel. open-appsec schema version "v1beta2".
# An install with no site using the engine leaves every host uninspected.
apiVersion: v1beta2

policies:
  default:
    mode: inactive
  specificRules: []
`)
}

// PolicyIsDefault reports whether content is already what DefaultPolicy renders,
// so an install that changes nothing writes nothing and makes the agent reload
// nothing.
func PolicyIsDefault(content []byte) bool {
	return string(content) == string(DefaultPolicy())
}

// Options are the seams this package runs through. Every field with a zero value
// has a production default, so cmd/panel sets only the ones that need the host,
// and a test sets all of them.
type Options struct {
	// Root is where the agent lives. Empty means FilesystemRoot; a test passes a
	// temporary directory.
	Root string
	// UnitName is the agent's systemd unit. Empty means UnitName.
	UnitName string
	// NginxUnit is the unit reloaded after an install. Empty means NginxUnitName.
	NginxUnit string
	// Codename is the Ubuntu codename whose archive is pinned. Empty reads
	// /etc/os-release.
	Codename string

	// Run runs one command. Nil means ExecRunner.
	Run Runner
	// ReadFile reads a file. Nil means os.ReadFile.
	ReadFile func(name string) ([]byte, error)
	// Stat checks a path. Nil means os.Stat.
	Stat func(name string) (os.FileInfo, error)
	// WriteFile writes a file. Nil means os.WriteFile.
	WriteFile func(name string, data []byte, perm os.FileMode) error
	// MkdirAll makes a directory tree. Nil means os.MkdirAll.
	MkdirAll func(name string, perm os.FileMode) error
	// MkdirTemp makes the private directory an archive is unpacked into. Nil means
	// os.MkdirTemp.
	MkdirTemp func(dir, pattern string) (string, error)
	// RemoveAll removes it again. Nil means os.RemoveAll.
	RemoveAll func(name string) error
	// Fetch downloads a pinned archive to dest. Nil means DefaultFetch.
	Fetch FetchFunc
	// ModulePresent reports whether the nginx build that serves has the
	// open-appsec attachment. Nil reports it as unknown, which reads as absent.
	ModulePresent func(ctx context.Context) (bool, error)
}

// Manager is the agent's lifecycle: install, uninstall and status.
type Manager struct {
	root          string
	unitName      string
	nginxUnit     string
	codename      string
	run           Runner
	readFile      func(string) ([]byte, error)
	stat          func(string) (os.FileInfo, error)
	writeFile     func(string, []byte, os.FileMode) error
	mkdirAll      func(string, os.FileMode) error
	mkdirTemp     func(string, string) (string, error)
	removeAll     func(string) error
	fetch         FetchFunc
	modulePresent func(context.Context) (bool, error)
	// ipcTimeout bounds the wait for the agent's IPC socket; ipcPoll is its step.
	ipcTimeout time.Duration
	ipcPoll    time.Duration
}

// New returns the manager.
func New(opts Options) *Manager {
	m := &Manager{
		root:          opts.Root,
		unitName:      opts.UnitName,
		nginxUnit:     opts.NginxUnit,
		codename:      opts.Codename,
		run:           opts.Run,
		readFile:      opts.ReadFile,
		stat:          opts.Stat,
		writeFile:     opts.WriteFile,
		mkdirAll:      opts.MkdirAll,
		mkdirTemp:     opts.MkdirTemp,
		removeAll:     opts.RemoveAll,
		fetch:         opts.Fetch,
		modulePresent: opts.ModulePresent,
	}
	if m.root == "" {
		m.root = FilesystemRoot
	}
	if m.unitName == "" {
		m.unitName = UnitName
	}
	if m.nginxUnit == "" {
		m.nginxUnit = NginxUnitName
	}
	if m.run == nil {
		m.run = ExecRunner{}
	}
	if m.readFile == nil {
		m.readFile = os.ReadFile
	}
	if m.ipcTimeout == 0 {
		m.ipcTimeout = 60 * time.Second
	}
	if m.ipcPoll == 0 {
		m.ipcPoll = time.Second
	}
	if m.stat == nil {
		m.stat = os.Stat
	}
	if m.writeFile == nil {
		m.writeFile = os.WriteFile
	}
	if m.mkdirAll == nil {
		m.mkdirAll = os.MkdirAll
	}
	if m.mkdirTemp == nil {
		m.mkdirTemp = os.MkdirTemp
	}
	if m.removeAll == nil {
		m.removeAll = os.RemoveAll
	}
	if m.fetch == nil {
		m.fetch = DefaultFetch
	}
	return m
}

// Root is where the agent lives.
func (m *Manager) Root() string { return m.root }

// Unit is the agent's systemd unit.
func (m *Manager) Unit() string { return m.unitName }

// Ctl is the vendor's control script under this manager's root.
func (m *Manager) Ctl() string { return filepath.Join(m.root, "scripts", "open-appsec-ctl.sh") }

// PolicyPath is the policy file this manager writes.
func (m *Manager) PolicyPath() string { return filepath.Join(m.root, "conf", "local_policy.yaml") }

// StatusCommand asks the agent for its own status report.
func (m *Manager) StatusCommand() Command {
	return Command{Name: m.Ctl(), Args: []string{"--status"}}
}

// ApplyPolicyCommand applies a policy file.
func (m *Manager) ApplyPolicyCommand(path string) Command {
	return Command{Name: m.Ctl(), Args: []string{"--apply-policy", path}}
}

// UninstallCommand is the vendor's uninstaller for this manager's root.
func (m *Manager) UninstallCommand() Command {
	return Command{Name: m.Ctl(), Args: []string{"--uninstall"}, Stdin: "Y\n"}
}

// ReloadNginxCommand reloads nginx so its workers register against the agent's
// current sockets.
func (m *Manager) ReloadNginxCommand() Command {
	return Command{Name: "systemctl", Args: []string{"reload", m.nginxUnit}}
}

// nginxActiveCommand asks systemd whether nginx runs. A stopped nginx has nothing
// to reload, and a host with no build yet is a normal state, not a failure.
func (m *Manager) nginxActiveCommand() Command {
	return Command{Name: "systemctl", Args: []string{"is-active", m.nginxUnit}}
}

func (m *Manager) unitStatusCommand() Command {
	return Command{Name: "systemctl", Args: []string{"show", "-p", "LoadState", "-p", "ActiveState", m.unitName}}
}

func (m *Manager) watchdogPath() string {
	return filepath.Join(m.root, "watchdog", "cp-nano-watchdog")
}

// Release is this host's pinned archive. The codename is read from /etc/os-release
// the first time it is needed, not at construction, so a manager can be built on a
// host without one.
func (m *Manager) Release() (Release, error) {
	if _, err := Architecture(); err != nil {
		return Release{}, err
	}
	codename := m.codename
	if codename == "" {
		c, err := m.readCodename()
		if err != nil {
			return Release{}, err
		}
		codename = c
	}
	return ReleaseFor(codename)
}

// readCodename reads VERSION_CODENAME (or UBUNTU_CODENAME) from /etc/os-release.
func (m *Manager) readCodename() (string, error) {
	data, err := m.readFile("/etc/os-release")
	if err != nil {
		return "", fmt.Errorf("appsec: read /etc/os-release: %w", err)
	}
	return parseCodename(string(data))
}

// parseCodename is readCodename's pure half, so the parsing is tested without a
// filesystem.
func parseCodename(osRelease string) (string, error) {
	for line := range strings.SplitSeq(osRelease, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "VERSION_CODENAME", "UBUNTU_CODENAME":
			value = strings.Trim(value, `"'`)
			if value != "" {
				return value, nil
			}
		}
	}
	return "", errors.New("appsec: /etc/os-release names no Ubuntu codename; the agent archive cannot be chosen")
}

// Status reports the agent's state, reading systemd and the vendor's own report
// rather than inferring from the presence of files.
func (m *Manager) Status(ctx context.Context) (Status, error) {
	st := Status{}
	// The watchdog's path is the canonical one: the unit runs it and the uninstall
	// removes it, so its absence is the agent being absent rather than a partial
	// install.
	if _, err := m.stat(m.watchdogPath()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			st.Reason = "the open-appsec agent is not installed"
			return st, nil
		}
		return Status{}, fmt.Errorf("appsec: inspect %s: %w", m.root, err)
	}
	st.Installed = true

	// The unit's state is the only honest signal for "inspecting": an installed
	// agent whose unit is stopped lets every request through.
	cmd := m.unitStatusCommand()
	out, err := m.run.Run(ctx, cmd)
	if err != nil {
		st.Reason = "systemd could not report " + m.unitName + ": " + commandErrorText(err, out)
		return st, nil
	}
	loaded, state := ParseUnitStatus(string(out))
	st.State = state
	st.Active = state == "active"
	if !loaded {
		st.Reason = m.unitName + " is not installed in systemd"
		return st, nil
	}

	// The vendor's own report carries the version and the policy verdict. It is a
	// read; failing to produce it is reported as a missing version, not a failed
	// status, because the unit state above already answered the question that
	// matters.
	if out, err := m.run.Run(ctx, m.StatusCommand()); err == nil {
		text := string(out)
		st.Version = ParseCtlVersion(text)
		st.PolicyLoad = ParsePolicyLoad(text)
	}
	if !st.Active {
		st.Reason = m.unitName + " is " + state + "; the engine cannot inspect while the agent is down"
		return st, nil
	}
	return st, nil
}

// ModulePresent reports whether the serving nginx build has the attachment.
func (m *Manager) ModulePresent(ctx context.Context) (bool, error) {
	if m.modulePresent == nil {
		return false, nil
	}
	return m.modulePresent(ctx)
}

// Available reports whether the engine can be used right now, and why not when it
// cannot. It is the value the firewall's availability seam hands back.
func (m *Manager) Available(ctx context.Context) (bool, string) {
	hasModule, err := m.ModulePresent(ctx)
	if err != nil {
		return false, "the nginx build in use could not be checked: " + err.Error()
	}
	if !hasModule {
		return false, "the nginx build in use has no open-appsec attachment; build nginx on the Nginx page first"
	}
	st, err := m.Status(ctx)
	if err != nil {
		return false, err.Error()
	}
	if !st.Installed {
		return false, "the open-appsec agent is not installed; install it from the Store"
	}
	if !st.Active {
		return false, "the open-appsec agent is " + st.State + "; start " + m.unitName
	}
	return true, ""
}

// Availability is the Store's view: the availability verdict plus the agent's own
// state and the warning the operator must acknowledge.
func (m *Manager) Availability(ctx context.Context) Availability {
	a := Availability{Warning: Warning, Acknowledgement: Acknowledgement}
	if rel, err := m.Release(); err == nil {
		a.PinnedVersion = rel.Version
	}
	ok, reason := m.Available(ctx)
	a.Available, a.Reason = ok, reason

	// Read the agent's own state even when the module is missing: the Store shows
	// what is installed, and a host can have the agent without a build that loads
	// it.
	if st, err := m.Status(ctx); err == nil {
		a.Installed, a.AgentActive = st.Installed, st.Active
		a.AgentVersion, a.PolicyLoad = st.Version, st.PolicyLoad
	}
	return a
}

// commandErrorText prefers a command's own output to its exit error, because the
// vendor's scripts explain themselves on stdout and stderr.
func commandErrorText(err error, out []byte) string {
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return msg
	}
	if err != nil {
		return err.Error()
	}
	return ""
}
