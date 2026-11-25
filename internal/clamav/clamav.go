// Package clamav installs, removes and reports ClamAV, and runs on-demand scans
// with it.
//
// ClamAV comes from the distribution's own packages: unlike the open-appsec agent
// there is nothing to pin or download here, and apt verifies what it installs.
// The panel owns the lifecycle (install, signature bootstrap, uninstall) and one
// action, scanning a path. It does not decide what is scanned or when; callers
// such as the file manager name the path.
//
// Every command is an argument vector; the panel never builds a shell line.
package clamav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/portbyte-id/server-panel/internal/osdep"
)

// Packages are the Ubuntu packages that make up the service: the scanner and its
// libraries, the clamd daemon, clamdscan (its client, a separate package), and the freshclam signature updater. The daemon is
// what makes a scan fast; without it every scan loads the whole signature set.
var Packages = []string{"clamav", "clamav-daemon", "clamdscan", "clamav-freshclam"}

// Units and binaries, as the Ubuntu packages lay them out.
const (
	DaemonUnit    = "clamav-daemon.service"
	FreshclamUnit = "clamav-freshclam.service"
	ScanBinary    = "/usr/bin/clamscan"
	DaemonScanBin = "/usr/bin/clamdscan"
	FreshclamBin  = "/usr/bin/freshclam"
	DaemonSocket  = "/run/clamav/clamd.ctl"

	signatureLimit = 10 * time.Minute
)

var (
	// ErrNotInstalled is returned when an operation needs ClamAV and it is absent.
	ErrNotInstalled = errors.New("clamav: ClamAV is not installed; install it from the Store")
	// ErrScanBusy is returned when another scan is still running. A scan can use a
	// gigabyte of memory without the daemon, so scans do not overlap.
	ErrScanBusy = errors.New("clamav: another scan is still running")
	// ErrInfected is returned by Scan when the scan completed and found infected
	// files. The ScanResult is returned with it.
	ErrInfected = errors.New("clamav: infected files found")
)

// Status is what the panel knows about ClamAV on this host.
type Status struct {
	// Installed says the scanner binary is on disk.
	Installed bool `json:"installed"`
	// Version is ClamAV's version, e.g. "1.4.3", or "" when not installed.
	Version string `json:"version"`
	// SignatureVersion and SignatureDate describe the loaded signature database
	// as the scanner reports it; "" when there is none yet.
	SignatureVersion string `json:"signature_version"`
	SignatureDate    string `json:"signature_date"`
	// DaemonActive says clamav-daemon is running: scans use it when it is.
	DaemonActive bool `json:"daemon_active"`
	// UpdaterActive says clamav-freshclam is running: signatures stay current.
	UpdaterActive bool `json:"updater_active"`
	// Scanning says a scan started from the panel is running now.
	Scanning bool `json:"scanning"`
	// Reason says, in one sentence, what is wrong or missing. Empty when healthy.
	Reason string `json:"reason"`
}

// Ready reports whether a scan can run now.
func (s Status) Ready() bool { return s.Installed && s.SignatureVersion != "" }

// Availability is the Store card's view: the status and the packages an install
// would add.
type Availability struct {
	Status
	Packages []string `json:"packages"`
}

// Infection is one file a scan flagged.
type Infection struct {
	Path      string `json:"path"`
	Signature string `json:"signature"`
}

// ScanResult is what one scan found.
type ScanResult struct {
	Path     string      `json:"path"`
	Engine   string      `json:"engine"` // "clamd" or "clamscan"
	Scanned  int         `json:"scanned"`
	Infected []Infection `json:"infected"`
	Errors   []string    `json:"errors,omitempty"`
}

// Command is one external command.
type Command struct {
	Name string
	Args []string
}

// Runner runs a command and returns its combined output. The error is the exit
// error; a scanner's exit status 1 means "infected files found", so callers read
// the output as well.
type Runner interface {
	Run(ctx context.Context, c Command) ([]byte, error)
}

// ExecRunner runs commands on this host. DEBIAN_FRONTEND is set so dpkg never
// waits for an answer the panel cannot give.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, c Command) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Env = append(cmd.Environ(), "DEBIAN_FRONTEND=noninteractive")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// Options are the seams the manager runs through; the zero value of each is the
// production behaviour.
type Options struct {
	// Run runs commands. Nil means ExecRunner.
	Run Runner
	// Install installs distribution packages. Nil means osdep's package install via Run.
	Install func(ctx context.Context, pkgs []string) error
	// Stat checks a path. Nil means os.Stat.
	Stat func(name string) (os.FileInfo, error)
	// SocketWait and SocketPoll bound the wait for the daemon's socket after an
	// install: clamd loads the signatures before it listens, which takes a minute.
	SocketWait, SocketPoll time.Duration
	// RemoveAll removes the signature directory after an uninstall. Nil means
	// os.RemoveAll.
	RemoveAll func(name string) error
	// CountFiles counts the regular files under a path, for the daemon's scans:
	// clamdscan reports no file count of its own. Nil walks the tree.
	CountFiles func(path string) int
}

// Manager is the ClamAV lifecycle and scanner.
type Manager struct {
	run        Runner
	install    func(context.Context, []string) error
	stat       func(string) (os.FileInfo, error)
	removeAll  func(string) error
	countFiles func(string) int
	socketWait time.Duration
	socketPoll time.Duration
	scanning   atomic.Bool
}

// DatabaseDir is where the signatures live. The packages' purge leaves the
// downloaded .sign files in it, so uninstall removes the directory itself.
const DatabaseDir = "/var/lib/clamav"

// defaultCountFiles counts the regular files under path. Errors are skipped: it
// only sizes a scan, and the scanner reports anything it could not read.
func defaultCountFiles(path string) int {
	n := 0
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			n++
		}
		return nil
	})
	return n
}

// New returns the manager.
func New(o Options) *Manager {
	m := &Manager{run: o.Run, install: o.Install, stat: o.Stat, removeAll: o.RemoveAll, countFiles: o.CountFiles,
		socketWait: o.SocketWait, socketPoll: o.SocketPoll}
	if m.removeAll == nil {
		m.removeAll = os.RemoveAll
	}
	if m.countFiles == nil {
		m.countFiles = defaultCountFiles
	}
	if m.run == nil {
		m.run = ExecRunner{}
	}
	if m.stat == nil {
		m.stat = os.Stat
	}
	if m.socketWait == 0 {
		m.socketWait = 3 * time.Minute
	}
	if m.socketPoll == 0 {
		m.socketPoll = 2 * time.Second
	}
	if m.install == nil {
		m.install = m.installPackages
	}
	return m
}

// pkgRunner adapts the manager's Runner to osdep.Runner, so the distribution calls
// run through the same seam. The output tail rides on the error, so a failed
// package command still says why.
type pkgRunner struct{ run Runner }

func (p pkgRunner) Run(ctx context.Context, name string, args ...string) error {
	out, err := p.run.Run(ctx, Command{Name: name, Args: args})
	if err != nil {
		return fmt.Errorf("%w: %s", err, tail(string(out), 10))
	}
	return nil
}

func (m *Manager) installPackages(ctx context.Context, pkgs []string) error {
	if err := osdep.NewUbuntu(pkgRunner{m.run}).InstallPackages(ctx, pkgs); err != nil {
		return fmt.Errorf("clamav: install: %w", err)
	}
	return nil
}

// ParseVersion reads `clamscan --version`, which prints
// "ClamAV 1.4.3/27800/Tue Nov 25 08:00:00 2026". A host with no signatures yet
// prints the version alone, or a signature version of 0.
func ParseVersion(out string) (version, sigVersion, sigDate string) {
	line := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	rest, ok := strings.CutPrefix(line, "ClamAV ")
	if !ok {
		return "", "", ""
	}
	parts := strings.SplitN(rest, "/", 3)
	version = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		if n, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && n > 0 {
			sigVersion = strconv.Itoa(n)
		}
	}
	if len(parts) > 2 && sigVersion != "" {
		sigDate = strings.TrimSpace(parts[2])
	}
	return version, sigVersion, sigDate
}

// ParseScan reads a scanner's output. A flagged file is "<path>: <name> FOUND";
// a failure is "<path>: <reason> ERROR" or "ERROR: <reason>"; clamscan's summary
// carries "Scanned files: N". clamdscan prints no such line, so Scanned is 0 for
// the daemon.
func ParseScan(out string) (scanned int, infected []Infection, problems []string) {
	for raw := range strings.SplitSeq(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
		case strings.HasSuffix(line, " FOUND"):
			body := strings.TrimSuffix(line, " FOUND")
			// The last ": " splits the path from the signature: a path may hold one.
			path, sig, ok := strings.CutLast(body, ": ")
			if !ok {
				continue
			}
			infected = append(infected, Infection{Path: path, Signature: sig})
		case strings.HasSuffix(line, " ERROR") || strings.HasPrefix(line, "ERROR:"):
			problems = append(problems, line)
		default:
			if v, ok := strings.CutPrefix(line, "Scanned files:"); ok {
				scanned, _ = strconv.Atoi(strings.TrimSpace(v))
			}
		}
	}
	return scanned, infected, problems
}

func parseUnit(out string) (loaded bool, state string) {
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

func (m *Manager) unitActive(ctx context.Context, unit string) bool {
	out, err := m.run.Run(ctx, Command{Name: "systemctl", Args: []string{"show", "-p", "LoadState", "-p", "ActiveState", unit}})
	if err != nil {
		return false
	}
	loaded, state := parseUnit(string(out))
	return loaded && state == "active"
}

// Status reads the scanner and systemd rather than inferring from files.
func (m *Manager) Status(ctx context.Context) Status {
	st := Status{Scanning: m.scanning.Load()}
	if _, err := m.stat(ScanBinary); err != nil {
		st.Reason = "ClamAV is not installed"
		return st
	}
	st.Installed = true
	if out, err := m.run.Run(ctx, Command{Name: ScanBinary, Args: []string{"--version"}}); err == nil {
		st.Version, st.SignatureVersion, st.SignatureDate = ParseVersion(string(out))
	}
	st.DaemonActive = m.unitActive(ctx, DaemonUnit)
	st.UpdaterActive = m.unitActive(ctx, FreshclamUnit)
	switch {
	case st.SignatureVersion == "":
		st.Reason = "no virus signatures are loaded yet; the updater downloads them on first run"
	case !st.DaemonActive:
		st.Reason = DaemonUnit + " is not running; scans fall back to the slower standalone scanner"
	case !st.UpdaterActive:
		st.Reason = FreshclamUnit + " is not running; signatures will go stale"
	}
	return st
}

// Availability is the Store card's view.
func (m *Manager) Availability(ctx context.Context) Availability {
	return Availability{Status: m.Status(ctx), Packages: Packages}
}

// Install installs the packages, fetches the first signatures, starts the daemon
// and the updater, and waits for the daemon to listen. Every step is idempotent,
// so a re-run repairs a half-finished install.
func (m *Manager) Install(ctx context.Context, progress func(string)) error {
	say := progress
	if say == nil {
		say = func(string) {}
	}
	say("installing " + strings.Join(Packages, ", "))
	if err := m.install(ctx, Packages); err != nil {
		return err
	}

	// The updater service holds freshclam's lock, so it is stopped while the first
	// download runs here with its output in the job log, and started again after.
	say("fetching the virus signatures (the first download is around 300 MB)")
	_, _ = m.run.Run(ctx, Command{Name: "systemctl", Args: []string{"stop", FreshclamUnit}})
	fctx, cancel := context.WithTimeout(ctx, signatureLimit)
	out, err := m.run.Run(fctx, Command{Name: FreshclamBin})
	cancel()
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		// freshclam tries to tell clamd to reload, which the packaged clamd.conf
		// disables. It is noise here: the daemon is restarted below, and later it
		// reloads changed signatures itself (SelfCheck, hourly).
		if line != "" && !strings.Contains(line, "Clamd was NOT notified") {
			say(line)
		}
	}
	if err != nil {
		return fmt.Errorf("clamav: freshclam could not download the signatures: %w: %s", err, tail(string(out), 10))
	}

	say("starting the updater and the daemon")
	if out, err := m.run.Run(ctx, Command{Name: "systemctl", Args: []string{"enable", "--now", FreshclamUnit}}); err != nil {
		return fmt.Errorf("clamav: enable %s: %w: %s", FreshclamUnit, err, tail(string(out), 10))
	}
	if out, err := m.run.Run(ctx, Command{Name: "systemctl", Args: []string{"enable", DaemonUnit}}); err != nil {
		return fmt.Errorf("clamav: enable %s: %w: %s", DaemonUnit, err, tail(string(out), 10))
	}
	// A restart, not a start: apt starts clamd at install time, before any
	// signatures exist, and that instance has nothing loaded.
	if out, err := m.run.Run(ctx, Command{Name: "systemctl", Args: []string{"restart", DaemonUnit}}); err != nil {
		return fmt.Errorf("clamav: start %s: %w: %s", DaemonUnit, err, tail(string(out), 10))
	}

	say("waiting for the daemon to load the signatures")
	if err := m.waitSocket(ctx); err != nil {
		return err
	}
	st := m.Status(ctx)
	if !st.Ready() || !st.DaemonActive {
		return fmt.Errorf("clamav: installed, but not healthy: %s", st.Reason)
	}
	say("ClamAV " + st.Version + ", signatures " + st.SignatureVersion + ", ready")
	return nil
}

func (m *Manager) waitSocket(ctx context.Context) error {
	deadline := time.Now().Add(m.socketWait)
	for {
		if fi, err := m.stat(DaemonSocket); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("clamav: %s is active but %s never appeared; check journalctl -u %s", DaemonUnit, DaemonSocket, DaemonUnit)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.socketPoll):
		}
	}
}

// Uninstall purges the packages, which also removes the signature database.
func (m *Manager) Uninstall(ctx context.Context, progress func(string)) error {
	say := progress
	if say == nil {
		say = func(string) {}
	}
	if !m.Status(ctx).Installed {
		return ErrNotInstalled
	}
	if m.scanning.Load() {
		return ErrScanBusy
	}
	say("removing " + strings.Join(Packages, ", "))
	// --autoremove takes the libraries and base package only ClamAV needed.
	if err := osdep.NewUbuntu(pkgRunner{m.run}).PurgePackages(ctx, Packages); err != nil {
		return fmt.Errorf("clamav: purge: %w", err)
	}
	if m.Status(ctx).Installed {
		return fmt.Errorf("clamav: the packages were purged but %s is still present", ScanBinary)
	}
	if err := m.removeAll(DatabaseDir); err != nil {
		return fmt.Errorf("clamav: remove %s: %w", DatabaseDir, err)
	}
	say("done")
	return nil
}

// Scan scans path, which must be absolute and already validated by the caller.
// It uses the daemon when it is running, otherwise the standalone scanner. A
// completed scan that finds infected files returns the result and ErrInfected.
func (m *Manager) Scan(ctx context.Context, path string, progress func(string)) (ScanResult, error) {
	say := progress
	if say == nil {
		say = func(string) {}
	}
	res := ScanResult{Path: path}
	if !strings.HasPrefix(path, "/") {
		return res, fmt.Errorf("clamav: scan path %q is not absolute", path)
	}
	st := m.Status(ctx)
	if !st.Installed {
		return res, ErrNotInstalled
	}
	if st.SignatureVersion == "" {
		return res, errors.New("clamav: no virus signatures are loaded yet; wait for the updater")
	}
	if !m.scanning.CompareAndSwap(false, true) {
		return res, ErrScanBusy
	}
	defer m.scanning.Store(false)

	cmd := Command{Name: ScanBinary, Args: []string{"--recursive", "--infected", path}}
	res.Engine = "clamscan"
	if st.DaemonActive {
		// --fdpass hands clamd an open descriptor, so root-owned files the clamav
		// user cannot read are still scanned.
		cmd = Command{Name: DaemonScanBin, Args: []string{"--fdpass", "--multiscan", path}}
		res.Engine = "clamd"
	}
	// clamdscan prints no file count, so the daemon's is the number of regular
	// files under the path when the scan started.
	files := 0
	if res.Engine == "clamd" {
		files = m.countFiles(path)
	}
	say(fmt.Sprintf("scanning %s with %s", path, res.Engine))
	out, runErr := m.run.Run(ctx, cmd)
	scanned, infected, problems := ParseScan(string(out))
	if res.Engine == "clamd" {
		scanned = files
	}
	res.Scanned, res.Infected, res.Errors = scanned, infected, problems
	for _, f := range infected {
		say(f.Path + ": " + f.Signature)
	}
	for _, p := range problems {
		say(p)
	}

	// Exit status 0 is clean and 1 is "found something"; anything else, or a
	// failure to run at all, means the scan did not complete.
	if runErr != nil {
		if ee, isExit := errors.AsType[*exec.ExitError](runErr); !isExit || ee.ExitCode() != 1 {
			return res, fmt.Errorf("clamav: %s did not complete: %w: %s", res.Engine, runErr, tail(string(out), 10))
		}
	}
	if len(infected) > 0 {
		say(fmt.Sprintf("%d infected file(s) found; nothing was changed", len(infected)))
		return res, fmt.Errorf("%w: %d in %s", ErrInfected, len(infected), path)
	}
	say("no infected files found")
	return res, nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
