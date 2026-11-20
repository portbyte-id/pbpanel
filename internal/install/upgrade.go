package install

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/store"
)

const (
	// healthTimeout is how long a freshly started binary has to answer before
	// the upgrade is rolled back. Development plan step 0.11 names 30 s.
	healthTimeout = 30 * time.Second

	// healthInterval is how often the health probe runs inside that window.
	healthInterval = 500 * time.Millisecond

	// healthPath is the probe route. It answers without a session and without
	// touching the database, so a success means "this binary is serving", which
	// is exactly what the rollback decision needs.
	healthPath = "/api/health"

	// upgradeSuffix is appended to the live binary's name for the staged copy,
	// and to the health check's user agent so a host log shows which process
	// asked.
	upgradeSuffix = ".upgrade"

	// binaryMode is what the staged copy is made executable with before the
	// rename. The live file keeps whatever mode it already had, because rename
	// replaces the directory entry and the new file's mode comes with it.
	binaryMode os.FileMode = 0o755
)

// Upgrader replaces the installed binary and restarts the service, with the
// migrations checked before the swap and the binary rolled back if the new one
// does not come up.
type Upgrader struct {
	Layout Layout
	Runner interface {
		Run(ctx context.Context, name string, args ...string) error
	}

	// Euid is the effective-uid check, as on Installer.
	Euid func() int

	// Out receives progress lines.
	Out io.Writer

	// SourcePath is the new binary. Empty means the running executable
	// (os.Executable), which is the `panel upgrade` case: the operator copied
	// the new binary somewhere, ran it, and it is replacing what is installed.
	SourcePath string

	// Health probes the new binary. Nil means probeHealth over HTTPS on the
	// panel's own bind address and port. It is a field so the rollback test can
	// fail the probe without a listener.
	Health func(ctx context.Context) error

	// Timeout is the health window. Zero means healthTimeout.
	Timeout time.Duration
}

// UpgradeResult reports what the upgrade did, so the CLI can print it and a test
// can assert it.
type UpgradeResult struct {
	// Migrated lists the schema versions the new binary applied.
	Migrated []int
	// RolledBack reports that the new binary failed its health check and the
	// previous one was put back.
	RolledBack bool
}

// Upgrade runs the upgrade: check migrations, stage the new binary beside the
// old one, rename it into place, restart, wait for health, and roll back if the
// new binary does not answer.
//
// The order matters. The migration check runs first because a migration that
// fails must fail while the old binary is still installed; the restart happens
// after the rename because systemd execs the path, not an inode; and the health
// window runs after the restart because that is the only moment the new binary
// can prove it works.
func (u *Upgrader) Upgrade(ctx context.Context) (UpgradeResult, error) {
	var result UpgradeResult

	if u.Layout.Root == "" {
		u.Layout = DefaultLayout()
	}
	if u.Out == nil {
		u.Out = io.Discard
	}
	if u.Euid == nil {
		u.Euid = os.Geteuid
	}
	if u.Timeout == 0 {
		u.Timeout = healthTimeout
	}
	if u.Health == nil {
		u.Health = u.probeHealth
	}

	if uid := u.Euid(); uid != 0 {
		return result, fmt.Errorf("upgrade: must run as root (effective uid is %d); re-run it with sudo", uid)
	}

	live := u.Layout.BinaryPath()
	staged := live + upgradeSuffix

	source, err := u.sourcePath()
	if err != nil {
		return result, err
	}

	// Nothing to do when the new binary is the installed one: a re-run of
	// upgrade must not restart a healthy panel.
	same, err := sameFile(source, live)
	if err != nil {
		return result, err
	}
	if same {
		printf(u.Out, "  already running %s\n", source)
		return result, nil
	}

	printf(u.Out, "  checking migrations\n")
	if err := store.CheckMigrations(u.Layout.DBPath()); err != nil {
		return result, fmt.Errorf("upgrade: migrations would fail, keeping the installed binary: %w", err)
	}
	pending, err := store.Pending(u.Layout.DBPath())
	if err != nil {
		return result, fmt.Errorf("upgrade: read pending migrations: %w", err)
	}
	result.Migrated = pending

	printf(u.Out, "  staging %s\n", staged)
	if err := copyFile(source, staged, binaryMode); err != nil {
		_ = os.Remove(staged)
		return result, fmt.Errorf("upgrade: stage binary: %w", err)
	}

	// The previous binary is kept beside the new one rather than deleted: that
	// is what the rollback renames back, and it is also what an operator can
	// copy back by hand if the host cannot start the service at all.
	previous := live + ".previous"
	if err := os.Rename(live, previous); err != nil && !errors.Is(err, fs.ErrNotExist) {
		_ = os.Remove(staged)
		return result, fmt.Errorf("upgrade: move the installed binary aside: %w", err)
	}
	if err := os.Rename(staged, live); err != nil {
		// Put the old binary back before returning, so a failed rename does not
		// leave the host without one.
		if rerr := os.Rename(previous, live); rerr != nil {
			return result, fmt.Errorf("upgrade: install the new binary: %w (and restoring the old one failed: %v)", err, rerr)
		}
		return result, fmt.Errorf("upgrade: install the new binary: %w", err)
	}

	// A host installed under the old binary name still has that name in
	// ExecStart: restarting first would put the new binary in place while the
	// unit went on pointing at the old path, and the next boot would run
	// whatever is left there. The unit is prepared before the restart so that
	// the restart is the one the health check covers.
	if err := u.reconcileUnits(ctx); err != nil {
		return result, u.rollback(previous, live, err, &result)
	}

	printf(u.Out, "  restarting %s\n", unitName)
	if err := u.Runner.Run(ctx, "systemctl", "restart", unitName); err != nil {
		return result, u.rollback(previous, live, fmt.Errorf("upgrade: restart: %w", err), &result)
	}
	if err := u.relinkPATH(); err != nil {
		return result, err
	}
	printf(u.Out, "  waiting up to %s for a health check\n", u.Timeout)
	if err := u.waitHealthy(ctx); err != nil {
		return result, u.rollback(previous, live, fmt.Errorf("upgrade: the new binary did not become healthy: %w", err), &result)
	}

	// The new binary is healthy, so the old one is no longer the rollback.
	_ = os.Remove(previous)
	return result, nil
}

// reconcileUnits rewrites the systemd units when their ExecStart no longer
// names the installed binary, which is the rename from panel to pbp on a host
// installed before it. It reloads systemd but never restarts a unit: the
// caller restarts panel.service once, over the unit as rewritten here.
//
// The firewall unit is rewritten but never restarted: it is a oneshot that only
// matters at the next boot.
func (u *Upgrader) reconcileUnits(ctx context.Context) error {
	changed, err := reconcileFile(u.Layout.UnitPath, UnitFile(u.Layout.BinaryPath()))
	if err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}
	fwPath := filepath.Join(filepath.Dir(u.Layout.UnitPath), firewallUnitName)
	fwChanged, err := reconcileFile(fwPath, FirewallUnitFile(u.Layout.BinaryPath()))
	if err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}
	if !changed && !fwChanged {
		return nil
	}
	printf(u.Out, "  updating systemd units for %s\n", BinaryName)
	if err := u.Runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("upgrade: daemon-reload: %w", err)
	}
	return nil
}

// relinkPATH puts BinaryName on PATH, which a host installed before the rename
// does not have. A symlink whose target is already right is left alone.
func (u *Upgrader) relinkPATH() error {
	if u.Layout.LinkPath == "" {
		return nil
	}
	if have, err := os.Readlink(u.Layout.LinkPath); err == nil &&
		filepath.Clean(have) == filepath.Clean(u.Layout.BinaryPath()) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(u.Layout.LinkPath), 0o755); err != nil {
		return fmt.Errorf("upgrade: create %s: %w", filepath.Dir(u.Layout.LinkPath), err)
	}
	// Remove only a symlink: a regular file at that name belongs to someone
	// else, and replacing it is not upgrade's decision to make.
	if info, err := os.Lstat(u.Layout.LinkPath); err == nil {
		if info.Mode()&fs.ModeSymlink == 0 {
			return fmt.Errorf("upgrade: %s exists and is not a symlink; move it aside to let upgrade link %s", u.Layout.LinkPath, BinaryName)
		}
		if err := os.Remove(u.Layout.LinkPath); err != nil {
			return fmt.Errorf("upgrade: replace %s: %w", u.Layout.LinkPath, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("upgrade: inspect %s: %w", u.Layout.LinkPath, err)
	}
	if err := os.Symlink(u.Layout.BinaryPath(), u.Layout.LinkPath); err != nil {
		return fmt.Errorf("upgrade: link %s: %w", u.Layout.LinkPath, err)
	}
	return nil
}

// reconcileFile writes want to path when the file differs, and reports whether
// it wrote. A missing file is written: an absent unit is a host install never
// finished for.
func reconcileFile(path, want string) (bool, error) {
	have, err := os.ReadFile(path)
	if err == nil && string(have) == want {
		return false, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// rollback puts previous back at live, restarts the service, and returns the
// error that caused it. The cause is returned rather than the rollback's own
// error, because the cause is what the operator has to act on.
func (u *Upgrader) rollback(previous, live string, cause error, result *UpgradeResult) error {
	result.RolledBack = true

	if err := os.Rename(previous, live); err != nil {
		return fmt.Errorf("%w; and the rollback failed, copy %s back to %s by hand: %v", cause, previous, live, err)
	}
	if err := u.Runner.Run(context.Background(), "systemctl", "restart", unitName); err != nil {
		return fmt.Errorf("%w; the previous binary is back but the service did not restart: %v", cause, err)
	}
	return fmt.Errorf("%w; the previous binary has been restored and the service restarted", cause)
}

// waitHealthy polls until the probe succeeds or the window closes.
func (u *Upgrader) waitHealthy(ctx context.Context) error {
	deadline := time.Now().Add(u.Timeout)
	var last error
	for {
		if err := u.Health(ctx); err == nil {
			return nil
		} else {
			last = err
		}
		if time.Now().After(deadline) {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthInterval):
		}
	}
}

// sourcePath is the binary being installed.
func (u *Upgrader) sourcePath() (string, error) {
	if u.SourcePath != "" {
		return u.SourcePath, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("upgrade: locate the running binary: %w", err)
	}
	return exe, nil
}

// probeHealth asks the panel's own listener whether it is serving.
//
// The certificate is the one install generated, so it is self-signed: the probe
// verifies nothing rather than trusting the system roots, which is what makes it
// work on a host whose certificate has never been trusted by anything. The probe
// proves "a panel is answering on this address", and the daemon restart is what
// proves "it is the new binary".
func (u *Upgrader) probeHealth(ctx context.Context) error {
	addr, err := u.listenAddr(ctx)
	if err != nil {
		return err
	}
	url := "https://" + addr + healthPath

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", url, resp.StatusCode)
	}
	return nil
}

// listenAddr is where the panel answers, from the settings registry.
func (u *Upgrader) listenAddr(ctx context.Context) (string, error) {
	st, err := store.Open(u.Layout.DBPath())
	if err != nil {
		return "", err
	}
	defer func() { _ = st.Close() }()

	reg, err := settings.New(st)
	if err != nil {
		return "", err
	}
	bind, err := reg.Str(ctx, "panel.bind")
	if err != nil {
		return "", err
	}
	port, err := reg.Int(ctx, "panel.port")
	if err != nil {
		return "", err
	}
	// A wildcard bind is not a destination; loopback is.
	if bind == "0.0.0.0" || bind == "::" || bind == "" {
		bind = "127.0.0.1"
	}
	return net.JoinHostPort(bind, strconv.Itoa(port)), nil
}

// copyFile writes src to dst with mode, fsyncing before the rename so a crash
// between the two leaves either no file or a complete one.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// The mode is set explicitly: OpenFile's is subject to the umask, and the
	// unit has to be able to exec the result.
	return os.Chmod(dst, mode)
}

// sameFile reports whether a and b are the same file on disk.
func sameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, fmt.Errorf("upgrade: read %s: %w", a, err)
	}
	bi, err := os.Stat(b)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("upgrade: read %s: %w", b, err)
	}
	return os.SameFile(ai, bi), nil
}
