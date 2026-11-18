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
	"github.com/portbyte-id/server-panel/internal/store"
)

// Errors a caller maps to a status.
var (
	// ErrUnknownVersion is a version that is neither supported nor known.
	ErrUnknownVersion = errors.New("php: no such version")
	// ErrNotReady is a version whose image has not been pulled.
	ErrNotReady = errors.New("php: the version is not installed; pull it first")
	// ErrBusy is a pull requested while one is running.
	ErrBusy = errors.New("php: the version is already being pulled")
	// ErrInUse is a removal refused because a site runs on the version.
	ErrInUse = errors.New("php: a site uses this version; move the site to another version first")
)

// State is where a version's image is.
type State string

// Version states.
const (
	StateAbsent  State = "absent"
	StatePulling State = "pulling"
	StateReady   State = "ready"
	StateFailed  State = "failed"
)

// DefaultVersions is the default of the `php.versions` setting: the versions
// with an official `-fpm` image still receiving security fixes.
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
	// Image is the official reference and Digest the image ID of the build
	// served when the version was last pulled.
	Image  string `json:"image"`
	Digest string `json:"digest"`
	State  State  `json:"state"`
	// Output is the end of a failed pull's output.
	Output    string     `json:"output"`
	UpdatedAt *time.Time `json:"updated_at"`
	// Supported is false for a version that was pulled once and has since left
	// the `php.versions` setting. It keeps working; it cannot be pulled again.
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
	// Supported returns the versions of the `php.versions` setting.
	Supported func(ctx context.Context) ([]string, error)
	// Read defaults to os.ReadFile.
	Read func(string) ([]byte, error)
	Now  func() time.Time
	// Scratch, when set, holds build contexts in place of Paths.BuildDir. Tests
	// use it so no build writes under /www.
	Scratch string
	// Log defaults to slog.Default.
	Log *slog.Logger
}

// Manager owns the version catalogue, each version's configuration (the
// php.ini form, php.ini as raw text, and the files FPM starts on), and the
// builds of each version's image.
type Manager struct {
	st        *store.Store
	eng       Engine
	ap        Applier
	p         Paths
	supported func(ctx context.Context) ([]string, error)
	read      func(string) ([]byte, error)
	now       func() time.Time
	scratch   string
	log       *slog.Logger
	catalog   *images.Catalog

	mu sync.Mutex

	famMu   sync.Mutex
	fams    map[string]*images.Family
	digests map[string]string
}

// New returns a Manager.
func New(o Options) (*Manager, error) {
	if o.Store == nil || o.Engine == nil || o.Applier == nil {
		return nil, errors.New("php: store, engine and applier are required")
	}
	m := &Manager{
		st: o.Store, eng: o.Engine, ap: o.Applier, p: o.Paths, supported: o.Supported,
		read: o.Read, now: o.Now, scratch: o.Scratch, log: o.Log,
		catalog: images.NewCatalog(o.Store), fams: map[string]*images.Family{}, digests: map[string]string{},
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
// The service runs whichever build is current when a container has to be made.
func (m *Manager) Service(ver string, start bool) confedit.Service {
	return NewService(m.eng, m.p, ver, start, m.imageFunc(ver))
}

func (m *Manager) imageFunc(ver string) ImageFunc {
	return func(ctx context.Context) (string, error) { return m.CurrentTag(ctx, ver) }
}

// CurrentTag is the image reference a version runs. It is ErrNotReady when no
// build serves.
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

// ContainerState is a version's container as Docker reports it. The bool is
// false when there is no such container.
func (m *Manager) ContainerState(ctx context.Context, ver string) (docker.Container, bool, error) {
	return NewService(m.eng, m.p, ver, false, m.imageFunc(ver)).State(ctx)
}

// family returns the build family of a version, made on first use. Its hooks
// are the PHP ones; the build lifecycle itself is package images.
func (m *Manager) family(ver string) (*images.Family, error) {
	m.famMu.Lock()
	defer m.famMu.Unlock()
	if f, ok := m.fams[ver]; ok {
		return f, nil
	}
	f, err := images.NewFamily(images.FamilyOptions{
		Kind: images.KindPHP, Version: ver, Repo: Repo, Catalog: m.catalog, Engine: m.eng,
		Log: m.log, Now: m.now,
		Hooks: images.Hooks{
			Build: func(ctx context.Context, b images.Build, progress docker.Progress) error {
				return m.buildImage(ctx, ver, b, progress)
			},
			Check: func(ctx context.Context, b images.Build, serving bool) (string, error) {
				return m.check(ctx, ver, b, serving)
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

// buildDir is the build context of one build on the host.
func (m *Manager) buildDir(ver, id string) string {
	if m.scratch != "" {
		return filepath.Join(m.scratch, ver, id)
	}
	return filepath.FromSlash(m.p.BuildDir(ver, id))
}

// buildImage is the build hook: the official image is pulled fresh, the
// Dockerfile for the requested extensions is written to the build's context, and
// the context is built under the build's tag.
func (m *Manager) buildImage(ctx context.Context, ver string, b images.Build, progress docker.Progress) error {
	base, err := m.eng.ImagePull(ctx, BaseImage(ver), progress)
	if err != nil {
		return err
	}
	m.setBaseDigest(ver, base)
	exts, err := ParseExtensions(b.Extensions)
	if err != nil {
		return err
	}
	dir := m.buildDir(ver, b.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("php: build context: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), Dockerfile(ver, exts), 0o644); err != nil {
		return fmt.Errorf("php: write Dockerfile: %w", err)
	}
	_, err = m.eng.ImageBuild(ctx, docker.BuildOptions{
		Tag: b.Tag, ContextDir: dir, Labels: map[string]string{docker.LabelManaged: "true"},
	}, progress)
	return err
}

// setBaseDigest keeps what the official image resolved to on the last pull of
// a version. It is the digest the catalogue shows; a pull records it.
func (m *Manager) setBaseDigest(ver string, base docker.Image) {
	d := base.ID
	if len(base.RepoDigests) > 0 {
		d = base.RepoDigests[0]
	}
	m.famMu.Lock()
	defer m.famMu.Unlock()
	m.digests[ver] = d
}

func (m *Manager) baseDigest(ver string) string {
	m.famMu.Lock()
	defer m.famMu.Unlock()
	return m.digests[ver]
}

// check is the candidate test of a build, run in a one-shot container of the
// new image: `php-fpm -t` with the version's own config, then `php -m`, which
// must list every extension the build was asked for. Its output is returned with
// the error so a failed build shows why.
//
// A first build has no config yet (the FPM file and php.ini are written by the
// first apply, which then validates them with the image that serves), so only
// the modules are tested then.
func (m *Manager) check(ctx context.Context, ver string, b images.Build, serving bool) (string, error) {
	exts, err := ParseExtensions(b.Extensions)
	if err != nil {
		return "", err
	}
	ini := m.p.IniPath(ver)
	name := ContainerName(ver) + "-candidate"
	tests := [][]string{
		{"php-fpm", "-t", "-y", m.p.FPMConf(ver), "-c", ini},
		{"php", "-c", ini, "-d", "display_startup_errors=1", "-d", "log_errors=0", "-m"},
	}
	if !serving {
		tests = [][]string{{"php", "-d", "display_startup_errors=1", "-d", "log_errors=0", "-m"}}
	}
	var out strings.Builder
	for _, test := range tests {
		cfg := Spec(m.p, ver, b.Tag).Config()
		if !serving {
			// A module list needs no mounts, and the directories a mount names may not
			// exist yet: Docker would create them root-owned.
			cfg.Binds = nil
		}
		cfg.Entrypoint, cfg.Cmd = test[:1], test[1:]
		// A leftover from a daemon that died mid-check would hold the name.
		_ = m.eng.ContainerRemove(ctx, name, true)
		res, err := m.eng.RunOnce(ctx, name, cfg)
		if err != nil {
			return out.String(), fmt.Errorf("%s could not be run: %w", test[0], err)
		}
		out.WriteString(res.Output())
		if res.ExitCode != 0 {
			return out.String(), fmt.Errorf("%s rejected the build (exit %d)", test[0], res.ExitCode)
		}
		if iniTrouble.MatchString(res.Output()) {
			return out.String(), fmt.Errorf("%s reported a problem with the build", test[0])
		}
		if test[len(test)-1] == "-m" {
			if missing := missingModules(res.Output(), exts); len(missing) > 0 {
				return out.String(), fmt.Errorf("the build does not load: %s", strings.Join(missing, ", "))
			}
		}
	}
	return out.String(), nil
}

// swap is the promotion hook: the running container moves from one build to
// another. A version whose container is not running has nothing to move; its
// next start uses the new build.
func (m *Manager) swap(ctx context.Context, ver, from, to string) error {
	svc := NewService(m.eng, m.p, ver, true, func(context.Context) (string, error) { return to, nil })
	c, found, err := svc.State(ctx)
	if err != nil {
		return err
	}
	if !found || !c.State.Running {
		return nil
	}
	return images.Swap(ctx, m.eng, images.SwapPlan{
		Name:    ContainerName(ver),
		Mount:   m.p.Root,
		Spec:    func(image string) docker.Spec { return Spec(m.p, ver, image) },
		Healthy: svc.Healthy,
	}, from, to)
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

// Usable reports whether sites can run on a version: its image is present.
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

// StartPull marks a supported version as pulling. It is called before the job
// is started, so two requests cannot both start one.
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
// none for a version that has never been built. A pull without a list of
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

// Pull installs a version: the official image is pulled, a build with the
// given extensions is made, checked and served. Call StartPull first. A build
// that fails leaves the version failed with its output, unless a build was
// already serving, which keeps serving.
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
	// `pulling` could never be pulled again.
	bg := context.WithoutCancel(ctx)
	_, has, err := fam.Current(bg)
	if err != nil {
		return errors.Join(buildErr, err)
	}
	v := Version{Version: ver, Image: BaseImage(ver)}
	if has {
		v.State, v.Digest = StateReady, m.baseDigest(ver)
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

// Remove uninstalls a version: its container is stopped and removed and its
// images deleted. Configuration, php.ini and settings are kept, so installing
// the version again brings them back. inUse reports whether a site runs on the
// version; a removal is refused while one does.
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
	if err := fam.Remove(ctx, func(ctx context.Context) error { return m.retire(ctx, ver) }); err != nil {
		return err
	}
	return m.setState(context.WithoutCancel(ctx), Version{Version: ver, Image: BaseImage(ver), State: StateAbsent})
}

// retire stops and removes a version's container; one that is not there is fine.
func (m *Manager) retire(ctx context.Context, ver string) error {
	name := ContainerName(ver)
	if err := m.eng.ContainerStop(ctx, name, 20*time.Second); err != nil && !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	if err := m.eng.ContainerRemove(ctx, name, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	return nil
}

// Reconcile corrects the catalogue at daemon start. A pull that was running is
// failed, and so is a build its family left running. A version that was ready
// and has an image from before builds had IDs keeps that image as its current
// build; a ready version with no image is absent again.
func (m *Manager) Reconcile(ctx context.Context) error {
	rows, err := m.rows(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		v := row
		if v.State == StatePulling {
			v.State, v.Output = StateFailed, "the pull was interrupted by a daemon restart"
		}
		legacy := ""
		if row.State == StateReady {
			legacy = LegacyImage(v.Version)
		}
		fam, err := m.family(v.Version)
		if err != nil {
			return err
		}
		serving, err := fam.Reconcile(ctx, legacy)
		if err != nil {
			return err
		}
		switch {
		case serving:
			v.State, v.Output = StateReady, ""
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

// productionIni reads the image's php.ini-production from the version's
// current build.
func (m *Manager) productionIni(ctx context.Context, ver string) ([]byte, error) {
	img, err := m.CurrentTag(ctx, ver)
	if err != nil {
		return nil, err
	}
	name := ContainerName(ver) + "-ini"
	_ = m.eng.ContainerRemove(ctx, name, true)
	res, err := m.eng.RunOnce(ctx, name, docker.ContainerConfig{
		Image:      img,
		Entrypoint: []string{"cat"},
		Cmd:        []string{"/usr/local/etc/php/php.ini-production"},
		Labels:     map[string]string{docker.LabelManaged: "true"},
	})
	if err != nil {
		return nil, fmt.Errorf("php: read php.ini-production of %s: %w", ver, err)
	}
	if res.ExitCode != 0 || len(res.Stdout) == 0 || res.Truncated {
		return nil, fmt.Errorf("php: read php.ini-production of %s: exit %d: %s", ver, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return res.Stdout, nil
}

// EnsureContainer creates, recreates or starts a version's container on its
// current build.
func (m *Manager) EnsureContainer(ctx context.Context, ver string) (docker.EnsureResult, error) {
	img, err := m.CurrentTag(ctx, ver)
	if err != nil {
		return docker.EnsureResult{}, err
	}
	return docker.Ensure(ctx, m.eng, Spec(m.p, ver, img), m.p.Root)
}

// ApplyMounts brings every running version's container up to its spec, as
// nginx.Runtime.ApplyMounts does for nginx. Stopped and absent containers are
// left alone.
func (m *Manager) ApplyMounts(ctx context.Context) error {
	vs, err := m.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, v := range vs {
		c, ok, err := m.ContainerState(ctx, v.Version)
		if err != nil {
			errs = append(errs, fmt.Errorf("php %s: %w", v.Version, err))
			continue
		}
		if !ok || !c.State.Running {
			continue
		}
		if _, err := m.EnsureContainer(ctx, v.Version); err != nil {
			errs = append(errs, fmt.Errorf("php %s: %w", v.Version, err))
		}
	}
	return errors.Join(errs...)
}

// BaseFiles returns the files a version's FPM starts on that are missing or
// out of date: php-fpm.conf and the form's ini (both panel-owned, rendered),
// and php.ini when it does not exist yet (copied from the image once; after
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
// version the file does not exist yet, and the image's php.ini-production,
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
