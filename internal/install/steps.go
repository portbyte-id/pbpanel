package install

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/portbyte-id/server-panel/internal/auth"
	"github.com/portbyte-id/server-panel/internal/httpapi"
	"github.com/portbyte-id/server-panel/internal/osdep"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/store"
)

// StepResult is one line of the install report: the step's name and whether it
// had anything left to do.
type StepResult struct {
	Name    string
	Skipped bool
}

// Report is what install tells the operator. GeneratedPassword is set only on
// the run that created the admin account; a re-run leaves it empty because there
// is no password to show and none was touched.
type Report struct {
	URL               string
	Username          string
	GeneratedPassword string
	Steps             []StepResult
}

// Installer runs the install steps. Every field with a zero value has a
// production default, so `cmd/panel` sets Layout, OS and Runner and leaves the
// rest.
type Installer struct {
	Layout Layout
	OS     osdep.OS
	Runner osdep.Runner

	// Euid is the effective-uid check. It is a field so the refusal is testable
	// on a machine where the tests do not run as root.
	Euid func() int

	// Out receives the report: progress lines, and the password when this run
	// creates the account.
	Out io.Writer

	// Rand is the entropy source for secret.key. Tests pass a fixed reader.
	Rand io.Reader

	// Password is the admin password to set when this run creates the account.
	// Empty means generate one. It is never used when an account already exists.
	Password string

	// Hostname is a name the panel certificate is issued for. Empty means
	// os.Hostname.
	Hostname string
}

// step is one entry of the install list.
//
// done is the skip-if-done check. A step whose done returns true runs no command
// and touches no file, which is what makes a second install silent: the
// idempotency test asserts exactly that through the Runner.
type step struct {
	name string
	done func(context.Context) (bool, error)
	run  func(context.Context) error
}

// runState is what the steps share while one install runs.
type runState struct {
	*Installer

	username string
	password string // set only when this run created the account

	// unitChanged records that this run rewrote panel.service, so the service
	// must be restarted rather than merely started.
	unitChanged bool

	url string
}

// Install runs the step list in order and returns the report.
//
// The list is the contract: one ordered slice, every entry named and carrying a
// done-check, and the tests pin both the order and the argv each step emits.
func (i *Installer) Install(ctx context.Context) (Report, error) {
	if i.Layout.Root == "" {
		i.Layout = DefaultLayout()
	}
	if i.Out == nil {
		i.Out = io.Discard
	}
	if i.Rand == nil {
		i.Rand = rand.Reader
	}
	if i.Euid == nil {
		i.Euid = os.Geteuid
	}

	s := &runState{Installer: i}
	steps := s.steps()

	report := Report{Steps: make([]StepResult, 0, len(steps))}
	for _, st := range steps {
		skip, err := st.done(ctx)
		if err != nil {
			return report, fmt.Errorf("install: %s: %w", st.name, err)
		}
		report.Steps = append(report.Steps, StepResult{Name: st.name, Skipped: skip})
		if skip {
			continue
		}
		printf(i.Out, "  %s\n", st.name)
		if err := st.run(ctx); err != nil {
			return report, fmt.Errorf("install: %s: %w", st.name, err)
		}
	}

	report.URL = s.url
	report.Username = s.username
	report.GeneratedPassword = s.password
	return report, nil
}

// steps is the install list, in the order of development plan step 0.11.
func (s *runState) steps() []step {
	return []step{
		{name: "root", done: always, run: s.checkRoot},
		{name: "distro", done: always, run: s.checkDistro},
		{name: "directories", done: s.dirsDone, run: s.createDirs},
		{name: "www-user", done: s.userDone, run: s.ensureUser},
		{name: "docker", done: s.dockerDone, run: s.ensureDocker},
		{name: "secret-key", done: s.secretKeyDone, run: s.writeSecretKey},
		{name: "database", done: s.databaseDone, run: s.initDatabase},
		{name: "admin-password", done: s.adminDone, run: s.createAdmin},
		{name: "panel-cert", done: s.certDone, run: s.generateCert},
		{name: "unit-file", done: s.unitDone, run: s.writeUnit},
		{name: "enable-service", done: s.enabledDone, run: s.enableService},
		{name: "start-service", done: s.activeDone, run: s.startService},
		{name: "print-url", done: always, run: s.printURL},
	}
}

// always is the done-check of a step with no "already done" state: the checks
// and the final print run on every install, and none of them mutates.
func always(context.Context) (bool, error) { return false, nil }

// --- root and distro -------------------------------------------------------

func (s *runState) checkRoot(context.Context) error {
	if uid := s.Euid(); uid != 0 {
		return fmt.Errorf("must run as root (effective uid is %d); re-run it with sudo", uid)
	}
	return nil
}

func (s *runState) checkDistro(context.Context) error {
	distro, err := s.OS.DetectDistro()
	if err != nil {
		return err
	}
	printf(s.Out, "    %s\n", distro)
	return nil
}

// --- directories -----------------------------------------------------------

func (s *runState) dirsDone(context.Context) (bool, error) {
	for _, d := range s.Layout.dirs() {
		info, err := os.Stat(d.path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
		if !info.IsDir() {
			return false, fmt.Errorf("%s exists and is not a directory", d.path)
		}
		if !modeOK(info.Mode(), d.mode) {
			return false, nil
		}
	}
	return true, nil
}

func (s *runState) createDirs(context.Context) error {
	for _, d := range s.Layout.dirs() {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return fmt.Errorf("create %s: %w", d.path, err)
		}
		// MkdirAll leaves an existing directory's mode alone, so a loosened mode
		// is repaired here rather than ignored.
		if err := os.Chmod(d.path, d.mode); err != nil {
			return fmt.Errorf("chmod %s: %w", d.path, err)
		}
	}
	return nil
}

// --- www user --------------------------------------------------------------

// userDone asks the host whether the account already exists. It is a read
// (getent), so running it on every install costs nothing and a second run issues
// no useradd.
func (s *runState) userDone(ctx context.Context) (bool, error) {
	return userExists(ctx, s.Runner, WWWUser)
}

func (s *runState) ensureUser(ctx context.Context) error {
	return s.OS.EnsureUser(ctx, WWWUser, WWWUID, WWWGID)
}

// userExists probes the account with getent, which reads the same database
// useradd writes to on every distribution osdep supports. A probe that fails for
// any reason reports "absent" and lets EnsureUser make the real decision, which
// fails loudly instead of creating a duplicate.
func userExists(ctx context.Context, r osdep.Runner, name string) (bool, error) {
	return r.Run(ctx, "getent", "passwd", name) == nil, nil
}

// --- docker ----------------------------------------------------------------

// dockerDone asks systemd whether the engine is already running. A probe that
// cannot run is "not done", so install tries the real thing rather than skipping
// on a guess.
func (s *runState) dockerDone(ctx context.Context) (bool, error) {
	return s.Runner.Run(ctx, "systemctl", "is-active", "--quiet", "docker") == nil, nil
}

func (s *runState) ensureDocker(ctx context.Context) error {
	return s.OS.EnsureDocker(ctx)
}

// --- secret key ------------------------------------------------------------

func (s *runState) secretKeyDone(context.Context) (bool, error) {
	info, err := os.Stat(s.Layout.SecretKeyPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return modeOK(info.Mode(), fileModeSecret), nil
}

// writeSecretKey creates data/secret.key once. An existing key is never
// rewritten: rotating it would invalidate everything it protects, and a
// re-install is a repair, not a reset. A key whose mode has been loosened is
// tightened in place.
func (s *runState) writeSecretKey(context.Context) error {
	path := s.Layout.SecretKeyPath()
	if _, err := os.Stat(path); err == nil {
		return os.Chmod(path, fileModeSecret)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	key := make([]byte, secretKeyBytes)
	if _, err := io.ReadFull(s.Rand, key); err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	if err := os.WriteFile(path, key, fileModeSecret); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// WriteFile's mode is subject to the umask, so the exact mode is set after.
	return os.Chmod(path, fileModeSecret)
}

// --- database --------------------------------------------------------------

func (s *runState) databaseDone(context.Context) (bool, error) {
	if _, err := os.Stat(s.Layout.DBPath()); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	pending, err := store.Pending(s.Layout.DBPath())
	if err != nil {
		return false, err
	}
	return len(pending) == 0, nil
}

func (s *runState) initDatabase(context.Context) error {
	st, err := store.Open(s.Layout.DBPath())
	if err != nil {
		return err
	}
	return st.Close()
}

// --- admin password --------------------------------------------------------

func (s *runState) adminDone(ctx context.Context) (bool, error) {
	if _, err := os.Stat(s.Layout.DBPath()); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	st, err := store.Open(s.Layout.DBPath())
	if err != nil {
		return false, err
	}
	defer func() { _ = st.Close() }()

	has, err := auth.OfflineService(st).HasAdmin(ctx)
	if err != nil {
		return false, err
	}
	return has, nil
}

// createAdmin sets the one admin account. It runs only when the account does not
// exist, so a re-install can never reset a password the operator has since
// changed.
func (s *runState) createAdmin(ctx context.Context) error {
	st, err := store.Open(s.Layout.DBPath())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	password := s.Password
	if password == "" {
		password, err = auth.GeneratePassword()
		if err != nil {
			return err
		}
	}
	if err := auth.OfflineService(st).SetPassword(ctx, auth.DefaultUsername, password); err != nil {
		return err
	}

	s.username = auth.DefaultUsername
	s.password = password
	return nil
}

// --- certificate -----------------------------------------------------------

func (s *runState) certDone(context.Context) (bool, error) {
	if _, err := tls.LoadX509KeyPair(s.Layout.CertPath(), s.Layout.KeyPath()); err != nil {
		return false, nil
	}
	return true, nil
}

// generateCert writes a self-signed pair, issued for the names an operator can
// actually type: loopback, the hostname, and the panel's bind address when it is
// not a wildcard.
func (s *runState) generateCert(ctx context.Context) error {
	hosts, err := s.certHosts(ctx)
	if err != nil {
		return err
	}
	return httpapi.GenerateSelfSigned(httpapi.CertPaths{
		CertFile: s.Layout.CertPath(),
		KeyFile:  s.Layout.KeyPath(),
	}, hosts)
}

func (s *runState) certHosts(ctx context.Context) ([]string, error) {
	hosts := []string{"127.0.0.1", "::1"}

	hostname := s.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	if hostname != "" {
		hosts = append(hosts, hostname)
	}

	// panel.bind is a setting, so the database has to exist by now. That is why
	// the certificate step runs after the database step.
	if bind, err := s.bindAddr(ctx); err == nil && bind != "" && bind != "0.0.0.0" && bind != "::" {
		hosts = append(hosts, bind)
	}
	return hosts, nil
}

// --- unit file -------------------------------------------------------------

func (s *runState) unitDone(context.Context) (bool, error) {
	have, err := os.ReadFile(s.Layout.UnitPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return string(have) == UnitFile(s.Layout.BinaryPath()), nil
}

// writeUnit writes panel.service and reloads systemd. The reload happens only
// when the file actually changed, which is what keeps a second install from
// touching the running daemon.
func (s *runState) writeUnit(ctx context.Context) error {
	dir := filepath.Dir(s.Layout.UnitPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.WriteFile(s.Layout.UnitPath, []byte(UnitFile(s.Layout.BinaryPath())), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", s.Layout.UnitPath, err)
	}
	s.unitChanged = true
	return s.Runner.Run(ctx, "systemctl", "daemon-reload")
}

// --- enable and start ------------------------------------------------------

func (s *runState) enabledDone(ctx context.Context) (bool, error) {
	return s.Runner.Run(ctx, "systemctl", "is-enabled", "--quiet", unitName) == nil, nil
}

func (s *runState) enableService(ctx context.Context) error {
	return s.Runner.Run(ctx, "systemctl", "enable", unitName)
}

// activeDone is true only when the service is running and this run did not
// rewrite its unit: a changed unit needs a restart to take effect, and a start
// would be a no-op against a process still running the old one.
func (s *runState) activeDone(ctx context.Context) (bool, error) {
	if s.unitChanged {
		return false, nil
	}
	return s.Runner.Run(ctx, "systemctl", "is-active", "--quiet", unitName) == nil, nil
}

func (s *runState) startService(ctx context.Context) error {
	return s.Runner.Run(ctx, "systemctl", "restart", unitName)
}

// --- report ----------------------------------------------------------------

func (s *runState) printURL(ctx context.Context) error {
	url, err := s.panelURL(ctx)
	if err != nil {
		return err
	}
	s.url = url

	printf(s.Out, "\nThe panel is installed.\n\n")
	if s.password != "" {
		printf(s.Out, "    username  %s\n", s.username)
		printf(s.Out, "    password  %s\n", s.password)
		printf(s.Out, "\nThe password is shown once and stored only as an argon2id hash.\n")
		printf(s.Out, "Change it from the panel after logging in.\n\n")
	} else {
		printf(s.Out, "    account   %s (password unchanged)\n\n", s.username)
	}
	printf(s.Out, "    url       %s\n\n", url)
	printf(s.Out, "The certificate is self-signed: the browser will warn once.\n")
	return nil
}

// --- database reads --------------------------------------------------------

func (s *runState) openRegistry(ctx context.Context) (*store.Store, *settings.Registry, error) {
	st, err := store.Open(s.Layout.DBPath())
	if err != nil {
		return nil, nil, err
	}
	reg, err := settings.New(st)
	if err != nil {
		_ = st.Close()
		return nil, nil, err
	}
	return st, reg, nil
}

func (s *runState) bindAddr(ctx context.Context) (string, error) {
	st, reg, err := s.openRegistry(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = st.Close() }()
	return reg.Str(ctx, "panel.bind")
}

// panelURL is the address to print. The bind address is used as stored: a
// wildcard bind is reachable on every interface, and inventing a hostname here
// would print a URL that may not resolve.
func (s *runState) panelURL(ctx context.Context) (string, error) {
	st, reg, err := s.openRegistry(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = st.Close() }()

	bind, err := reg.Str(ctx, "panel.bind")
	if err != nil {
		return "", err
	}
	port, err := reg.Int(ctx, "panel.port")
	if err != nil {
		return "", err
	}
	return "https://" + net.JoinHostPort(bind, strconv.Itoa(port)), nil
}

// printf writes a progress line. The write error is discarded for the same
// reason cmd/panel's printf discards it: every call is on the way out of the
// process, and the exit code carries the outcome.
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
