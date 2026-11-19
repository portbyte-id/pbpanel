package php

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
	"github.com/portbyte-id/server-panel/internal/native"
	"github.com/portbyte-id/server-panel/internal/store"
)

// Errors a caller maps to a status.
var (
	// ErrUnknownVersion is a version that is neither supported nor known.
	ErrUnknownVersion = errors.New("php: no such version")
	// ErrNotReady is a version whose build has not been made.
	ErrNotReady = errors.New("php: the version is not installed; build it first")
	// ErrBusy is a build requested while one is running.
	ErrBusy = errors.New("php: the version is already being built")
	// ErrInUse is a removal refused because a site runs on the version.
	ErrInUse = errors.New("php: a site uses this version; move the site to another version first")
)

// State is where a version's build is.
type State string

// Version states.
const (
	StateAbsent  State = "absent"
	StatePulling State = "pulling"
	StateReady   State = "ready"
	StateFailed  State = "failed"
)

// DefaultVersions is the default of the `php.versions` setting: the versions
// still receiving security fixes.
const DefaultVersions = "8.2,8.3,8.4,8.5"

// ParseVersions reads the `php.versions` setting: versions separated by commas.
func ParseVersions(csv string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for v := range strings.SplitSeq(csv, ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if err := CheckVersion(v); err != nil {
			return nil, err
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("php: at least one version is required")
	}
	sortVersions(out)
	return out, nil
}

func sortVersions(vs []string) {
	key := func(v string) int {
		major, minor, _ := strings.Cut(v, ".")
		a, _ := strconv.Atoi(major)
		b, _ := strconv.Atoi(minor)
		return a*100 + b
	}
	sort.Slice(vs, func(i, j int) bool { return key(vs[i]) < key(vs[j]) })
}

// Version is one row of the catalogue.
type Version struct {
	Version string `json:"version"`
	// Image is the release the serving build compiled (8.3.37), and Digest the
	// sha256 of the source tarball it was made from. A version nothing serves
	// names its series reference and has no digest.
	Image  string `json:"image"`
	Digest string `json:"digest"`
	State  State  `json:"state"`
	// Output is the end of a failed build's output.
	Output    string     `json:"output"`
	UpdatedAt *time.Time `json:"updated_at"`
	// Supported is false for a version that was installed once and has since
	// left the `php.versions` setting. It keeps working; it cannot be built again.
	Supported bool `json:"supported"`
	// Current is the build the version serves, nil when none does.
	Current *images.Build `json:"current"`
}

// Applier is the part of confedit.Editor the manager uses.
type Applier interface {
	Apply(ctx context.Context, ch confedit.Change) (confedit.Result, error)
}

// Options configures a Manager.
type Options struct {
	Store   *store.Store
	Engine  Engine
	Applier Applier
	Paths   Paths
	// Build compiles one build. It defaults to the native builder over Engine and
	// Paths: Docker compiles, the host runs the result.
	Build func(ctx context.Context, id, version string, exts []Extension, progress docker.Progress) error
	// Run runs host commands: the validators, systemctl. It defaults to
	// ExecRunner.
	Run Runner
	// UnitDir is where unit files are written. Empty means systemd's own
	// directory; tests use a temporary one.
	UnitDir string
	// Supported returns the versions of the `php.versions` setting.
	Supported func(ctx context.Context) ([]string, error)
	// Read defaults to os.ReadFile.
	Read func(string) ([]byte, error)
	Now  func() time.Time
	// Log defaults to slog.Default.
	Log *slog.Logger
}

// Manager owns the version catalogue, each version's configuration (the
// php.ini form, php.ini as raw text, and the files FPM starts on), and the
// builds of each version.
type Manager struct {
	st        *store.Store
	ap        Applier
	p         Paths
	build     func(ctx context.Context, id, version string, exts []Extension, progress docker.Progress) error
	run       Runner
	unitDir   string
	supported func(ctx context.Context) ([]string, error)
	read      func(string) ([]byte, error)
	now       func() time.Time
	log       *slog.Logger
	catalog   *images.Catalog

	mu sync.Mutex

	famMu sync.Mutex
	fams  map[string]*images.Family
}

// New returns a Manager.
func New(o Options) (*Manager, error) {
	if o.Store == nil || o.Engine == nil || o.Applier == nil {
		return nil, errors.New("php: store, engine and applier are required")
	}
	m := &Manager{
		st: o.Store, ap: o.Applier, p: o.Paths, build: o.Build, run: o.Run,
		unitDir: o.UnitDir, supported: o.Supported, read: o.Read, now: o.Now, log: o.Log,
		catalog: images.NewCatalog(o.Store), fams: map[string]*images.Family{},
	}
	if m.build == nil {
		nb := NewBuilder(o.Engine, o.Paths)
		m.build = nb.Build
	}
	if m.run == nil {
		m.run = ExecRunner{}
	}
	if m.unitDir == "" {
		m.unitDir = DefaultUnitDir
	}
	if m.supported == nil {
		m.supported = func(context.Context) ([]string, error) { return ParseVersions(DefaultVersions) }
	}
	if m.read == nil {
		m.read = os.ReadFile
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.log == nil {
		m.log = slog.Default()
	}
	return m, nil
}

// Paths returns where PHP's files live.
func (m *Manager) Paths() Paths { return m.p }

// Service returns the confedit service of a version. See NewService for start.
func (m *Manager) Service(ver string, start bool) confedit.Service {
	return NewService(m.run, m.p, ver, start).WithUnitDir(m.unitDir)
}

// runtime is a version's systemd runtime: the unit, whether it runs, and the
// stop the operator chose. The flag lives beside the build catalogue, so it
// survives a panel restart.
func (m *Manager) runtime(ver string) *Runtime {
	return NewRuntime(m.p, m.run, ver).WithUnitDir(m.unitDir).WithFlags(m.catalog)
}

// Status is a version's unit as systemd reports it. The bool is false when the
// unit does not exist yet.
func (m *Manager) Status(ctx context.Context, ver string) (native.Status, bool, error) {
	if err := CheckVersion(ver); err != nil {
		return native.Status{}, false, fmt.Errorf("%w: %s", ErrUnknownVersion, ver)
	}
	st, err := m.runtime(ver).Status(ctx)
	return st, st.Loaded, err
}

// CurrentTag is the build reference a version serves. It is ErrNotReady when
// none does.
func (m *Manager) CurrentTag(ctx context.Context, ver string) (string, error) {
	if err := CheckVersion(ver); err != nil {
		return "", fmt.Errorf("%w: %s", ErrUnknownVersion, ver)
	}
	fam, err := m.family(ver)
	if err != nil {
		return "", err
	}
	tag, err := fam.CurrentTag(ctx)
	if err != nil {
		return "", err
	}
	if tag == "" {
		return "", fmt.Errorf("%w: PHP %s has no build serving", ErrNotReady, ver)
	}
	return tag, nil
}

// family returns the build family of a version, made on first use. Its hooks
// are the PHP ones: compile natively, check the candidate's own binaries on the
// host, and swap the `current` link under a running unit. The build lifecycle
// itself is package images.
func (m *Manager) family(ver string) (*images.Family, error) {
	m.famMu.Lock()
	defer m.famMu.Unlock()
	if f, ok := m.fams[ver]; ok {
		return f, nil
	}
	f, err := images.NewFamily(images.FamilyOptions{
		Kind: images.KindPHP, Version: ver, Repo: Repo, Catalog: m.catalog,
		Engine: dirStore{p: m.p, ver: ver}, Log: m.log, Now: m.now,
		Hooks: images.Hooks{
			Build: func(ctx context.Context, b images.Build, progress docker.Progress) error {
				return m.buildHook(ctx, ver, b, progress)
			},
			Check: func(ctx context.Context, b images.Build, serving bool) (string, error) {
				return m.checkCandidate(ctx, ver, b)
			},
			Swap: func(ctx context.Context, from, to string) error { return m.swap(ctx, ver, from, to) },
		},
	})
	if err != nil {
		return nil, err
	}
	m.fams[ver] = f
	return f, nil
}

// buildHook compiles the build's prefix: a builder image matching the host makes
// it, and the panel reads it out and throws the image away.
func (m *Manager) buildHook(ctx context.Context, ver string, b images.Build, progress docker.Progress) error {
	exts, err := ParseExtensions(b.Extensions)
	if err != nil {
		return err
	}
	return m.build(ctx, b.ID, ver, exts, progress)
}

// configPresent reports whether the version has the files a candidate is tested
// against: the FPM config and php.ini. Before the first apply neither exists.
func (m *Manager) configPresent(ver string) bool {
	for _, f := range []string{m.p.FPMConf(ver), m.p.IniPath(ver)} {
		if _, err := m.read(f); err != nil {
			return false
		}
	}
	return true
}

// checkCandidate is the test of a new build: the candidate's own binaries on the
// host, against the live configuration, so a build that would not start is
// rejected before anything serves. `php -m` must list every extension the build
// was asked for. Its output is returned with the error so a failed build shows
// why.
//
// A first build has no config yet (the FPM file and php.ini are written by the
// first apply, which then validates them with the build that serves), so only
// the module list is tested then.
func (m *Manager) checkCandidate(ctx context.Context, ver string, b images.Build) (string, error) {
	exts, err := ParseExtensions(b.Extensions)
	if err != nil {
		return "", err
	}
	runEnv, ok := m.run.(native.EnvRunner)
	if !ok {
		return "", fmt.Errorf("php %s: the runner cannot set PHP_INI_SCAN_DIR, so conf.d would be ignored", ver)
	}
	ini := m.p.IniPath(ver)
	env := []string{scanDirEnv(m.p, ver)}
	tests := []struct {
		what string
		bin  string
		args []string
	}{
		{"php-fpm", m.p.BinaryOf(ver, b.ID), []string{"-t", "-y", m.p.FPMConf(ver), "-c", ini}},
		{"php", m.p.CLIBinaryOf(ver, b.ID), []string{"-c", ini, "-d", "display_startup_errors=1", "-d", "log_errors=0", "-m"}},
	}
	if !m.configPresent(ver) {
		tests = tests[1:]
		tests[0].args = []string{"-d", "display_startup_errors=1", "-d", "log_errors=0", "-m"}
	}
	var out strings.Builder
	for _, test := range tests {
		b, err := runEnv.RunEnv(ctx, env, test.bin, test.args...)
		out.Write(b)
		if err != nil {
			if code, ran := native.ExitCode(err); ran {
				return out.String(), fmt.Errorf("%s rejected the build (exit %d)", test.what, code)
			}
			return out.String(), fmt.Errorf("%s could not be run: %w", test.what, err)
		}
		if iniTrouble.MatchString(string(b)) {
			return out.String(), fmt.Errorf("%s reported a problem with the build", test.what)
		}
		if test.what == "php" {
			if missing := missingModules(string(b), exts); len(missing) > 0 {
				return out.String(), fmt.Errorf("the build does not load: %s", strings.Join(missing, ", "))
			}
		}
	}
	return out.String(), nil
}

// swap is the promotion hook: the version's `current` link moves to the new
// build, and a running unit is restarted onto it, since a binary cannot be
// swapped under a reload. A version whose unit is not running only needs the
// link: its next start uses the new build.
//
// When the restart does not hold, the link is put back and the unit restarted on
// the old build, so a failed swap leaves what served before serving.
func (m *Manager) swap(ctx context.Context, ver, from, to string) error {
	toID, err := native.IDOfTag(to, product)
	if err != nil {
		return err
	}
	prev, _ := readLink(m.p, ver)
	if prev == "" && from != "" {
		prev, _ = native.IDOfTag(from, product)
	}
	if err := setLink(m.p, ver, toID); err != nil {
		return err
	}
	st, err := m.runtime(ver).Status(ctx)
	if err != nil || !st.Active {
		return nil
	}
	if err := restartUnit(ctx, m.run, ver); err != nil {
		if prev == "" {
			return err
		}
		bg := context.WithoutCancel(ctx)
		if lerr := setLink(m.p, ver, prev); lerr != nil {
			return errors.Join(err, lerr)
		}
		if rerr := restartUnit(bg, m.run, ver); rerr != nil {
			return errors.Join(err, fmt.Errorf("putting build %s back: %w", prev, rerr))
		}
		return err
	}
	return nil
}

// --- catalogue ---------------------------------------------------------------

func (m *Manager) rows(ctx context.Context) (map[string]Version, error) {
	rows, err := m.st.DB().QueryContext(ctx, `SELECT version, image, digest, state, output, updated_at FROM php_versions`)
	if err != nil {
		return nil, fmt.Errorf("php: list versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]Version{}
	for rows.Next() {
		var v Version
		var at string
		if err := rows.Scan(&v.Version, &v.Image, &v.Digest, &v.State, &v.Output, &at); err != nil {
			return nil, fmt.Errorf("php: list versions: %w", err)
		}
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
			v.UpdatedAt = &t
		}
		out[v.Version] = v
	}
	return out, rows.Err()
}

// List returns the supported versions and any other version the panel has a
// row for, oldest first.
func (m *Manager) List(ctx context.Context) ([]Version, error) {
	sup, err := m.supported(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := m.rows(ctx)
	if err != nil {
		return nil, err
	}
	isSup := map[string]bool{}
	names := append([]string(nil), sup...)
	for _, v := range sup {
		isSup[v] = true
	}
	for v := range rows {
		if !isSup[v] {
			names = append(names, v)
		}
	}
	sortVersions(names)
	out := make([]Version, 0, len(names))
	for _, name := range names {
		v, ok := rows[name]
		if !ok {
			v = Version{Version: name, Image: BaseImage(name), State: StateAbsent}
		}
		v.Supported = isSup[name]
		fam, err := m.family(name)
		if err != nil {
			return nil, err
		}
		cur, has, err := fam.Current(ctx)
		if err != nil {
			return nil, err
		}
		if has {
			v.Current = &cur
		}
		out = append(out, v)
	}
	return out, nil
}

// Get returns one version.
func (m *Manager) Get(ctx context.Context, ver string) (Version, error) {
	if CheckVersion(ver) != nil {
		return Version{}, fmt.Errorf("%w: %s", ErrUnknownVersion, ver)
	}
	list, err := m.List(ctx)
	if err != nil {
		return Version{}, err
	}
	for _, v := range list {
		if v.Version == ver {
			return v, nil
		}
	}
	return Version{}, fmt.Errorf("%w: %s", ErrUnknownVersion, ver)
}

// Usable reports whether sites can run on a version: a build serves it.
func (m *Manager) Usable(ctx context.Context, ver string) error {
	v, err := m.Get(ctx, ver)
	if err != nil {
		return err
	}
	if v.State != StateReady {
		return fmt.Errorf("%w: PHP %s is %s", ErrNotReady, ver, v.State)
	}
	return nil
}

func (m *Manager) setState(ctx context.Context, v Version) error {
	_, err := m.st.DB().ExecContext(ctx, `
		INSERT INTO php_versions (version, image, digest, state, output, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(version) DO UPDATE SET image = excluded.image, digest = excluded.digest,
			state = excluded.state, output = excluded.output, updated_at = excluded.updated_at`,
		v.Version, v.Image, v.Digest, string(v.State), v.Output, m.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("php: record %s as %s: %w", v.Version, v.State, err)
	}
	return nil
}

// buildIdentity is what the catalogue records about a serving build: the release
// it compiled, and the sha256 of the source tarball it was made from. A build
// that recorded neither keeps the series reference and no digest. Both are files
// the builder wrote, so they are read from disk rather than through Read, which
// sees the config tree.
func (m *Manager) buildIdentity(ver, id string) (image, digest string) {
	image = BaseImage(ver)
	if rel, err := native.BuildVersion(filepath.FromSlash(m.p.VersionFile(ver, id)), id, product); err == nil && rel != "" {
		image = rel
	}
	if b, err := os.ReadFile(filepath.FromSlash(m.p.SourceSHA256File(ver, id))); err == nil {
		digest = strings.TrimSpace(string(b))
	}
	return image, digest
}

// StartPull marks a supported version as being installed. It is called before
// the job is started, so two requests cannot both start one.
func (m *Manager) StartPull(ctx context.Context, ver string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	v, err := m.Get(ctx, ver)
	if err != nil {
		return err
	}
	if !v.Supported {
		return fmt.Errorf("%w: %s is not in the supported versions", ErrUnknownVersion, ver)
	}
	if v.State == StatePulling {
		return ErrBusy
	}
	fam, err := m.family(ver)
	if err != nil {
		return err
	}
	if err := fam.Idle(); err != nil {
		return ErrBusy
	}
	v.State, v.Output, v.Image = StatePulling, "", BaseImage(ver)
	return m.setState(ctx, v)
}

// CurrentExtensions returns the extensions of the build a version serves, or
// none for a version that has never been built. An install without a list of
// extensions keeps them.
func (m *Manager) CurrentExtensions(ctx context.Context, ver string) ([]Extension, error) {
	fam, err := m.family(ver)
	if err != nil {
		return nil, err
	}
	cur, has, err := fam.Current(ctx)
	if err != nil || !has {
		return nil, err
	}
	return ParseExtensions(cur.Extensions)
}

// Pull installs a version: it is compiled with the given extensions, checked on
// the host and served. Call StartPull first. A build that fails leaves the
// version failed with its output, unless a build was already serving, which
// keeps serving.
func (m *Manager) Pull(ctx context.Context, ver string, exts []Extension, progress docker.Progress) error {
	if err := CheckVersion(ver); err != nil {
		return err
	}
	fam, err := m.family(ver)
	if err != nil {
		return err
	}
	b, buildErr := fam.Build(ctx, ExtensionNames(exts), progress)

	// The outcome is recorded even when the job was canceled: a version left
	// `pulling` could never be installed again.
	bg := context.WithoutCancel(ctx)
	cur, has, err := fam.Current(bg)
	if err != nil {
		return errors.Join(buildErr, err)
	}
	v := Version{Version: ver, Image: BaseImage(ver)}
	if has {
		v.State = StateReady
		v.Image, v.Digest = m.buildIdentity(ver, cur.ID)
	} else {
		v.State, v.Output = StateFailed, b.Output
	}
	if err := m.setState(bg, v); err != nil {
		return errors.Join(buildErr, err)
	}
	return buildErr
}

// StartRebuild checks what a rebuild needs before its job starts: the version
// must be serving, no build may be running, and the extensions must be valid.
// It returns the parsed extensions for the job.
func (m *Manager) StartRebuild(ctx context.Context, ver string, names []string) ([]Extension, error) {
	v, err := m.Get(ctx, ver)
	if err != nil {
		return nil, err
	}
	if v.State != StateReady {
		return nil, fmt.Errorf("%w: PHP %s is %s", ErrNotReady, ver, v.State)
	}
	fam, err := m.family(ver)
	if err != nil {
		return nil, err
	}
	if err := fam.Idle(); err != nil {
		return nil, ErrBusy
	}
	return ParseExtensions(names)
}

// Rebuild builds the version again with other extensions and serves the result
// when it passes. A failure does not change the version's state: the build that
// served keeps serving.
func (m *Manager) Rebuild(ctx context.Context, ver string, exts []Extension, progress docker.Progress) (images.Build, error) {
	if err := CheckVersion(ver); err != nil {
		return images.Build{}, err
	}
	fam, err := m.family(ver)
	if err != nil {
		return images.Build{}, err
	}
	return fam.Build(ctx, ExtensionNames(exts), progress)
}

// Builds returns a version's builds, newest first.
func (m *Manager) Builds(ctx context.Context, ver string) ([]images.Build, error) {
	if err := CheckVersion(ver); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownVersion, ver)
	}
	fam, err := m.family(ver)
	if err != nil {
		return nil, err
	}
	return fam.Builds(ctx)
}

// StartActivate checks an activation before its job starts: the version must
// be serving something, and no build may be running.
func (m *Manager) StartActivate(ctx context.Context, ver string) error {
	if err := m.Usable(ctx, ver); err != nil {
		return err
	}
	fam, err := m.family(ver)
	if err != nil {
		return err
	}
	if err := fam.Idle(); err != nil {
		return ErrBusy
	}
	return nil
}

// Activate makes an earlier ready build the one a version serves: the way back
// from a build that misbehaves.
func (m *Manager) Activate(ctx context.Context, ver, id string) (images.Build, error) {
	if err := CheckVersion(ver); err != nil {
		return images.Build{}, fmt.Errorf("%w: %s", ErrUnknownVersion, ver)
	}
	if err := images.CheckID(id); err != nil {
		return images.Build{}, fmt.Errorf("%w: %s", images.ErrNotFound, id)
	}
	fam, err := m.family(ver)
	if err != nil {
		return images.Build{}, err
	}
	return fam.Activate(ctx, id)
}

// Remove uninstalls a version: its unit is stopped, disabled and removed, and
// its builds are deleted. Configuration, php.ini and settings are kept, so
// installing the version again brings them back. inUse reports whether a site
// runs on the version; a removal is refused while one does.
func (m *Manager) Remove(ctx context.Context, ver string, inUse func(context.Context) (bool, error)) error {
	v, err := m.Get(ctx, ver)
	if err != nil {
		return err
	}
	if v.State == StatePulling {
		return ErrBusy
	}
	if inUse != nil {
		used, err := inUse(ctx)
		if err != nil {
			return err
		}
		if used {
			return ErrInUse
		}
	}
	fam, err := m.family(ver)
	if err != nil {
		return err
	}
	// The link goes first: the catalogue refuses to delete the build that serves,
	// and a removal leaves nothing serving. A link left behind would also name a
	// directory that is about to go.
	if err := os.Remove(filepath.FromSlash(m.p.CurrentLink(ver))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("php %s: remove %s: %w", ver, m.p.CurrentLink(ver), err)
	}
	if err := fam.Remove(ctx, func(ctx context.Context) error { return m.retire(ctx, ver) }); err != nil {
		return err
	}
	// Whatever is left: a build the catalogue never marked ready, an extraction
	// that did not finish.
	if err := os.RemoveAll(filepath.FromSlash(m.p.VersionsDir(ver))); err != nil {
		return fmt.Errorf("php %s: remove the builds: %w", ver, err)
	}
	return m.setState(context.WithoutCancel(ctx), Version{Version: ver, Image: BaseImage(ver), State: StateAbsent})
}

// retire stops and disables a version's unit and removes its unit file, so the
// next install writes a fresh one.
func (m *Manager) retire(ctx context.Context, ver string) error {
	name := UnitName(ver)
	_, _ = m.run.Run(ctx, "systemctl", "disable", name)
	if out, err := m.run.Run(ctx, "systemctl", "stop", name); err != nil && !strings.Contains(string(out), "not loaded") {
		return fmt.Errorf("php %s: systemctl stop: %w: %s", ver, err, strings.TrimSpace(string(out)))
	}
	file := filepath.Join(m.unitDir, name)
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("php %s: remove %s: %w", ver, file, err)
	}
	if out, err := m.run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("php %s: systemctl daemon-reload: %w: %s", ver, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Reconcile corrects the catalogue at daemon start. An install that was running
// is failed, and so is a build its family left running. A version that is ready
// and whose build is still on disk keeps it, and the `current` link is pointed
// back at it; a ready version whose build has gone is absent again.
func (m *Manager) Reconcile(ctx context.Context) error {
	rows, err := m.rows(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		v := row
		if v.State == StatePulling {
			v.State, v.Output = StateFailed, "the install was interrupted by a daemon restart"
		}
		fam, err := m.family(v.Version)
		if err != nil {
			return err
		}
		// No legacy image: a build is a directory on the host now, so an image a
		// version ran in before is not one, and step 5 retires the container.
		serving, err := fam.Reconcile(ctx, "")
		if err != nil {
			return err
		}
		switch {
		case serving:
			v.State, v.Output = StateReady, ""
			cur, has, err := fam.Current(ctx)
			if err != nil {
				return err
			}
			if has {
				if got, lerr := readLink(m.p, v.Version); lerr != nil || got != cur.ID {
					if err := setLink(m.p, v.Version, cur.ID); err != nil {
						return err
					}
				}
			}
		case v.State == StateReady:
			v.State, v.Output = StateAbsent, ""
		}
		if v.State != row.State || v.Output != row.Output {
			if err := m.setState(ctx, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- files -------------------------------------------------------------------

// productionIni reads the serving build's php.ini-production from disk. It is
// what a version's php.ini is first copied from.
func (m *Manager) productionIni(ctx context.Context, ver string) ([]byte, error) {
	tag, err := m.CurrentTag(ctx, ver)
	if err != nil {
		return nil, err
	}
	id, err := native.IDOfTag(tag, product)
	if err != nil {
		return nil, err
	}
	file := m.p.ProductionIniFile(ver, id)
	b, err := os.ReadFile(filepath.FromSlash(file))
	if err != nil {
		return nil, fmt.Errorf("php: read php.ini-production of %s: %w", ver, err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("php: %s is empty", file)
	}
	return b, nil
}

// EnsureRunning writes the version's unit, enables it and starts it when it is
// not running. A running version is left alone.
func (m *Manager) EnsureRunning(ctx context.Context, ver string) (EnsureResult, error) {
	if _, err := m.CurrentTag(ctx, ver); err != nil {
		return EnsureResult{}, err
	}
	return m.runtime(ver).EnsureRunning(ctx)
}

// ApplyUnits rewrites the unit of every version whose text changed and restarts
// the versions that are running: the native equivalent of recreating a container
// so it mounts new web roots. A version with no build, or one that is not
// running, is left alone.
func (m *Manager) ApplyUnits(ctx context.Context) error {
	vs, err := m.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, v := range vs {
		if err := m.runtime(v.Version).Apply(ctx); err != nil {
			errs = append(errs, fmt.Errorf("php %s: %w", v.Version, err))
		}
	}
	return errors.Join(errs...)
}

// BaseFiles returns the files a version's FPM starts on that are missing or
// out of date: php-fpm.conf and the form's ini (both panel-owned, rendered),
// and php.ini when it does not exist yet (copied from the build once; after
// that it is the operator's and never rewritten). Every change that touches a
// version includes them, so the first one provisions the directory.
func (m *Manager) BaseFiles(ctx context.Context, ver string) ([]confedit.File, error) {
	conf, err := m.p.RenderFPMConf(ver)
	if err != nil {
		return nil, err
	}
	s, err := m.Settings(ctx, ver)
	if err != nil {
		return nil, err
	}
	form, err := RenderFormIni(s)
	if err != nil {
		return nil, err
	}
	var files []confedit.File
	for _, f := range []confedit.File{
		{Path: m.p.FPMConf(ver), Content: conf},
		{Path: m.p.FormIni(ver), Content: form},
	} {
		have, err := m.read(f.Path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("php: read %s: %w", f.Path, err)
		}
		if err != nil || !bytes.Equal(have, f.Content) {
			files = append(files, f)
		}
	}
	if _, err := m.read(m.p.IniPath(ver)); errors.Is(err, fs.ErrNotExist) {
		ini, err := m.productionIni(ctx, ver)
		if err != nil {
			return nil, err
		}
		files = append(files, confedit.File{Path: m.p.IniPath(ver), Content: ini})
	} else if err != nil {
		return nil, fmt.Errorf("php: read %s: %w", m.p.IniPath(ver), err)
	}
	return files, nil
}

// --- settings form -----------------------------------------------------------

// Settings returns a version's stored form values, or the defaults.
func (m *Manager) Settings(ctx context.Context, ver string) (Settings, error) {
	var raw string
	err := m.st.DB().QueryRowContext(ctx, `SELECT json FROM php_settings WHERE version = ?`, ver).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("php: read settings of %s: %w", ver, err)
	}
	s := DefaultSettings()
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Settings{}, fmt.Errorf("php: stored settings of %s are damaged: %w", ver, err)
	}
	return s, nil
}

func (m *Manager) saveSettings(ctx context.Context, ver string, s Settings) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = m.st.DB().ExecContext(ctx,
		`INSERT INTO php_settings (version, json) VALUES (?, ?)
		 ON CONFLICT(version) DO UPDATE SET json = excluded.json`, ver, string(raw))
	return err
}

// SetSettings validates the form, stores it and applies it through confedit.
// As with nginx, the database is written first and put back if the apply fails.
func (m *Manager) SetSettings(ctx context.Context, ver string, s Settings) (confedit.Result, error) {
	if err := s.Validate(); err != nil {
		return confedit.Result{}, err
	}
	if err := m.Usable(ctx, ver); err != nil {
		return confedit.Result{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	prev, err := m.Settings(ctx, ver)
	if err != nil {
		return confedit.Result{}, err
	}
	if err := m.saveSettings(ctx, ver, s); err != nil {
		return confedit.Result{}, fmt.Errorf("php: store settings of %s: %w", ver, err)
	}
	undo := func(err error) (confedit.Result, error) {
		if rbErr := m.saveSettings(context.WithoutCancel(ctx), ver, prev); rbErr != nil {
			err = errors.Join(err, fmt.Errorf("php: restoring the stored settings: %w", rbErr))
		}
		return confedit.Result{}, err
	}
	files, err := m.BaseFiles(ctx, ver)
	if err != nil {
		return undo(err)
	}
	if len(files) == 0 {
		return confedit.Result{Versions: map[string]int{}}, nil
	}
	res, err := m.ap.Apply(ctx, confedit.Change{
		Files: files, Services: []confedit.Service{m.Service(ver, false)}, Reason: "php " + ver + " settings",
	})
	if err != nil {
		return undo(err)
	}
	return res, nil
}

// --- raw php.ini -------------------------------------------------------------

// ReadIni returns php.ini as it is on disk. Before the first change to a
// version the file does not exist yet, and the build's php.ini-production,
// which is what it will be created from, is returned instead.
func (m *Manager) ReadIni(ctx context.Context, ver string) ([]byte, error) {
	if err := m.Usable(ctx, ver); err != nil {
		return nil, err
	}
	b, err := m.read(m.p.IniPath(ver))
	if errors.Is(err, fs.ErrNotExist) {
		return m.productionIni(ctx, ver)
	}
	if err != nil {
		return nil, fmt.Errorf("php: read php.ini of %s: %w", ver, err)
	}
	return b, nil
}

// WriteIni replaces php.ini with the operator's text. `php-fpm -t` must accept
// it, or the old file is put back and the validator's output is in the error.
func (m *Manager) WriteIni(ctx context.Context, ver string, content []byte) (confedit.Result, error) {
	if err := m.Usable(ctx, ver); err != nil {
		return confedit.Result{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	base, err := m.BaseFiles(ctx, ver)
	if err != nil {
		return confedit.Result{}, err
	}
	files := []confedit.File{{Path: m.p.IniPath(ver), Content: content}}
	for _, f := range base {
		if f.Path != m.p.IniPath(ver) {
			files = append(files, f)
		}
	}
	return m.ap.Apply(ctx, confedit.Change{
		Files: files, Services: []confedit.Service{m.Service(ver, false)}, Reason: "edit php.ini " + ver,
	})
}
