package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

// upgradeHost is a host with a panel already installed and a new binary waiting
// to replace it.
type upgradeHost struct {
	t      *testing.T
	layout Layout
	runner *fakeRunner
	out    strings.Builder

	live    string
	source  string
	content string // what the live binary should hold after the upgrade
}

// newUpgradeHost builds the installed state: the binary directory from the
// layout, the live binary, and a distinct new binary elsewhere.
func newUpgradeHost(t *testing.T) *upgradeHost {
	t.Helper()
	h := newHost(t)
	if _, err := h.install(); err != nil {
		t.Fatalf("install: %v", err)
	}

	uh := &upgradeHost{
		t:       t,
		layout:  h.layout,
		runner:  h.runner,
		live:    h.layout.BinaryPath(),
		content: "new binary",
	}
	if err := os.WriteFile(uh.live, []byte("old binary"), 0o755); err != nil {
		t.Fatalf("write the installed binary: %v", err)
	}

	uh.source = filepath.Join(t.TempDir(), "panel-new")
	if err := os.WriteFile(uh.source, []byte(uh.content), 0o755); err != nil {
		t.Fatalf("write the new binary: %v", err)
	}
	uh.runner.reset()
	return uh
}

// upgrader wires the Upgrader to this host. health is the probe under test.
func (uh *upgradeHost) upgrader(health func(context.Context) error) *Upgrader {
	return &Upgrader{
		Layout:     uh.layout,
		Runner:     uh.runner,
		Euid:       func() int { return 0 },
		Out:        &uh.out,
		SourcePath: uh.source,
		Health:     health,
		Timeout:    50 * time.Millisecond,
	}
}

func (uh *upgradeHost) liveContents() string {
	data, err := os.ReadFile(uh.live)
	if err != nil {
		uh.t.Fatalf("read the installed binary: %v", err)
	}
	return string(data)
}

// TestUpgradeReplacesTheBinary is the happy path: the new binary is in place,
// executable, and the service was restarted.
func TestUpgradeReplacesTheBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are POSIX")
	}

	uh := newUpgradeHost(t)
	result, err := uh.upgrader(func(context.Context) error { return nil }).Upgrade(context.Background())
	if err != nil {
		t.Fatalf("Upgrade: %v\noutput:\n%s", err, uh.out.String())
	}
	if result.RolledBack {
		t.Fatal("a healthy upgrade reported a rollback")
	}

	if got := uh.liveContents(); got != uh.content {
		t.Fatalf("installed binary holds %q, want %q", got, uh.content)
	}
	info, err := os.Stat(uh.live)
	if err != nil {
		t.Fatalf("stat the installed binary: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("installed binary mode = %o, want 755", got)
	}

	if !containsCommand(uh.runner.names(), "systemctl restart panel.service") {
		t.Fatalf("upgrade never restarted the service:\n  %s",
			strings.Join(uh.runner.names(), "\n  "))
	}

	// The staged copy and the previous binary are cleaned up on success.
	if _, err := os.Stat(uh.live + upgradeSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staged binary is still on disk")
	}
	if _, err := os.Stat(uh.live + ".previous"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the previous binary is still on disk")
	}
}

// TestUpgradeRollsBackWhenHealthFails is the rollback requirement: a new binary
// that does not answer is replaced by the one that was there, and the service is
// restarted onto it.
func TestUpgradeRollsBackWhenHealthFails(t *testing.T) {
	uh := newUpgradeHost(t)

	failHealth := func(context.Context) error { return errors.New("connection refused") }
	result, err := uh.upgrader(failHealth).Upgrade(context.Background())
	if err == nil {
		t.Fatal("Upgrade with a failing health check returned no error")
	}
	if !result.RolledBack {
		t.Error("the result does not report the rollback")
	}
	if !strings.Contains(err.Error(), "did not become healthy") {
		t.Errorf("error = %v, want it to name the health check", err)
	}

	// The installed binary must be the old one again.
	if got := uh.liveContents(); got != "old binary" {
		t.Fatalf("installed binary holds %q after a rollback, want the old one", got)
	}

	// And the service must have been restarted onto it: two restarts, the new
	// binary's and the rollback's.
	restarts := 0
	for _, c := range uh.runner.names() {
		if c == "systemctl restart panel.service" {
			restarts++
		}
	}
	if restarts != 2 {
		t.Fatalf("the service was restarted %d time(s), want 2 (new binary, then rollback):\n  %s",
			restarts, strings.Join(uh.runner.names(), "\n  "))
	}
}

// TestUpgradeKeepsTheOldBinaryWhenMigrationsFail proves the dry check runs
// before the swap: a migration that would fail must leave the installed binary
// untouched and must not restart anything.
func TestUpgradeKeepsTheOldBinaryWhenMigrationsFail(t *testing.T) {
	uh := newUpgradeHost(t)

	// A schema version this binary does not know makes the check refuse.
	st, err := store.Open(uh.layout.DBPath())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if _, err := st.DB().Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (9999, '2026-11-16T00:00:00Z')`); err != nil {
		t.Fatalf("insert a future schema version: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err = uh.upgrader(func(context.Context) error { return nil }).Upgrade(context.Background())
	if err == nil {
		t.Fatal("Upgrade with an impossible migration returned no error")
	}
	if !strings.Contains(err.Error(), "migration") {
		t.Errorf("error = %v, want it to name the migrations", err)
	}

	if got := uh.liveContents(); got != "old binary" {
		t.Fatalf("installed binary holds %q after a refused upgrade, want the old one", got)
	}
	for _, c := range uh.runner.names() {
		if strings.HasPrefix(c, "systemctl restart") {
			t.Fatalf("restarted the service after refusing the upgrade:\n  %s",
				strings.Join(uh.runner.names(), "\n  "))
		}
	}
}

// TestUpgradeIsANoOpWhenTheSourceIsInstalled covers a re-run of upgrade on a
// binary that is already in place.
func TestUpgradeIsANoOpWhenTheSourceIsInstalled(t *testing.T) {
	uh := newUpgradeHost(t)

	in := uh.upgrader(func(context.Context) error { return nil })
	in.SourcePath = uh.live

	result, err := in.Upgrade(context.Background())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if result.RolledBack {
		t.Error("a no-op upgrade reported a rollback")
	}
	if got := uh.liveContents(); got != "old binary" {
		t.Fatalf("the installed binary was rewritten: %q", got)
	}
	if len(uh.runner.calls) != 0 {
		t.Fatalf("a no-op upgrade ran commands:\n  %s",
			strings.Join(uh.runner.names(), "\n  "))
	}
}

// TestUpgradeRequiresRoot mirrors the install check.
func TestUpgradeRequiresRoot(t *testing.T) {
	uh := newUpgradeHost(t)
	in := uh.upgrader(func(context.Context) error { return nil })
	in.Euid = func() int { return 1000 }

	if _, err := in.Upgrade(context.Background()); err == nil {
		t.Fatal("upgrade as a non-root user returned no error")
	}
	if got := uh.liveContents(); got != "old binary" {
		t.Fatalf("upgrade changed the binary as a non-root user: %q", got)
	}
	if len(uh.runner.calls) != 0 {
		t.Fatalf("ran commands as a non-root user:\n  %s",
			strings.Join(uh.runner.names(), "\n  "))
	}
}

// TestUpgradeMovesAnOldHostOntoTheNewName is the migration path: a host
// installed as `panel` has a unit that execs it and no PATH entry, and an
// upgrade must leave both pointing at `pbp`.
func TestUpgradeMovesAnOldHostOntoTheNewName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the PATH entry is a symlink")
	}

	uh := newUpgradeHost(t)

	// Put the host back in its pre-rename state: the unit names the old binary.
	oldUnit := UnitFile(filepath.Join(uh.layout.BinDir(), "panel"))
	if err := os.WriteFile(uh.layout.UnitPath, []byte(oldUnit), 0o644); err != nil {
		t.Fatalf("write the old unit: %v", err)
	}
	if err := os.Remove(uh.layout.LinkPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove the PATH entry: %v", err)
	}
	uh.runner.reset()

	if _, err := uh.upgrader(func(context.Context) error { return nil }).Upgrade(context.Background()); err != nil {
		t.Fatalf("Upgrade: %v\noutput:\n%s", err, uh.out.String())
	}

	unit, err := os.ReadFile(uh.layout.UnitPath)
	if err != nil {
		t.Fatalf("read the unit: %v", err)
	}
	if want := UnitFile(uh.layout.BinaryPath()); string(unit) != want {
		t.Errorf("unit after upgrade:\n%s\nwant:\n%s", unit, want)
	}
	if !containsCommand(uh.runner.names(), "systemctl daemon-reload") {
		t.Error("the unit changed but systemd was never reloaded")
	}
	// One restart, over the rewritten unit: a second one would mean the unit
	// was applied after the health check had already passed.
	restarts := 0
	for _, c := range uh.runner.names() {
		if c == "systemctl restart panel.service" {
			restarts++
		}
	}
	if restarts != 1 {
		t.Errorf("panel.service restarted %d time(s), want 1:\n  %s", restarts, strings.Join(uh.runner.names(), "\n  "))
	}

	target, err := os.Readlink(uh.layout.LinkPath)
	if err != nil {
		t.Fatalf("%s is not a symlink after upgrade: %v", uh.layout.LinkPath, err)
	}
	if target != uh.layout.BinaryPath() {
		t.Errorf("%s points at %q, want %q", uh.layout.LinkPath, target, uh.layout.BinaryPath())
	}
}

// TestUpgradeLeavesACorrectUnitAlone keeps the migration from becoming churn: a
// host already on the new name must not have its unit rewritten or the daemon
// reloaded.
func TestUpgradeLeavesACorrectUnitAlone(t *testing.T) {
	uh := newUpgradeHost(t)
	uh.runner.reset()

	if _, err := uh.upgrader(func(context.Context) error { return nil }).Upgrade(context.Background()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if containsCommand(uh.runner.names(), "systemctl daemon-reload") {
		t.Errorf("a correct unit was rewritten:\n  %s", strings.Join(uh.runner.names(), "\n  "))
	}
}

func containsCommand(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}
