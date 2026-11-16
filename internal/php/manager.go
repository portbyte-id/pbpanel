package php

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
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
	// Image is the official reference and Digest what it resolved to when pulled.
	Image  string `json:"image"`
	Digest string `json:"digest"`
	State  State  `json:"state"`
	// Output is the end of a failed pull's output.
	Output    string     `json:"output"`
	UpdatedAt *time.Time `json:"updated_at"`
	// Supported is false for a version that was pulled once and has since left
	// the `php.versions` setting. It keeps working; it cannot be pulled again.
	Supported bool `json:"supported"`
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
}

// Manager owns the version catalogue and each version's configuration: the
// php.ini form, php.ini as raw text, and the files FPM starts on.
type Manager struct {
	st        *store.Store
	eng       Engine
	ap        Applier
	p         Paths
	supported func(ctx context.Context) ([]string, error)
	read      func(string) ([]byte, error)
	now       func() time.Time

	mu sync.Mutex
}

// New returns a Manager.
func New(o Options) (*Manager, error) {
	if o.Store == nil || o.Engine == nil || o.Applier == nil {
		return nil, errors.New("php: store, engine and applier are required")
	}
	m := &Manager{st: o.Store, eng: o.Engine, ap: o.Applier, p: o.Paths, supported: o.Supported, read: o.Read, now: o.Now}
	if m.supported == nil {
		m.supported = func(context.Context) ([]string, error) { return ParseVersions(DefaultVersions) }
	}
	if m.read == nil {
		m.read = os.ReadFile
	}
	if m.now == nil {
		m.now = time.Now
	}
	return m, nil
}

// Paths returns where PHP's files live.
func (m *Manager) Paths() Paths { return m.p }

// Service returns the confedit service of a version. See NewService for start.
func (m *Manager) Service(ver string, start bool) confedit.Service {
	return NewService(m.eng, m.p, ver, start)
}

// ContainerState is a version's container as Docker reports it. The bool is
// false when there is no such container.
func (m *Manager) ContainerState(ctx context.Context, ver string) (docker.Container, bool, error) {
	return NewService(m.eng, m.p, ver, false).State(ctx)
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
	v.State, v.Output, v.Image = StatePulling, "", BaseImage(ver)
	return m.setState(ctx, v)
}

// pullTail is how many lines of a failed pull are kept with the version. The
// whole output is in the job; this is what the PHP page shows without it.
const pullTail = 30

// Pull is the body of the pull job: it pulls the official image, builds the
// derived one, and records the version as ready, or as failed with the end of
// the output. Call StartPull first.
func (m *Manager) Pull(ctx context.Context, ver string, progress docker.Progress) error {
	if err := CheckVersion(ver); err != nil {
		return err
	}
	var tail []string
	emit := func(line string) {
		if tail = append(tail, line); len(tail) > pullTail {
			tail = tail[1:]
		}
		if progress != nil {
			progress(line)
		}
	}

	v := Version{Version: ver, Image: BaseImage(ver)}
	err := func() error {
		base, err := m.eng.ImagePull(ctx, BaseImage(ver), emit)
		if err != nil {
			return err
		}
		if len(base.RepoDigests) > 0 {
			v.Digest = base.RepoDigests[0]
		} else {
			v.Digest = base.ID
		}
		_, err = m.eng.ImageBuild(ctx, docker.BuildOptions{
			Tag: Image(ver), Dockerfile: Dockerfile(ver),
			Labels: map[string]string{docker.LabelManaged: "true"},
		}, emit)
		return err
	}()

	// The outcome is recorded even when the job was canceled: a version left
	// `pulling` could never be pulled again.
	bg := context.WithoutCancel(ctx)
	if err != nil {
		v.State = StateFailed
		v.Output = strings.Join(append(tail, err.Error()), "\n")
		return errors.Join(err, m.setState(bg, v))
	}
	v.State = StateReady
	return m.setState(bg, v)
}

// Reconcile corrects the catalogue at daemon start: a pull that was running
// when the daemon stopped is failed, and a version whose image is no longer on
// the host is absent again.
func (m *Manager) Reconcile(ctx context.Context) error {
	rows, err := m.rows(ctx)
	if err != nil {
		return err
	}
	for _, v := range rows {
		switch v.State {
		case StatePulling:
			v.State, v.Output = StateFailed, "the pull was interrupted by a daemon restart"
		case StateReady:
			if _, err := m.eng.ImageInspect(ctx, Image(v.Version)); !errors.Is(err, docker.ErrNotFound) {
				if err != nil {
					return err
				}
				continue
			}
			v.State, v.Output = StateAbsent, ""
		default:
			continue
		}
		if err := m.setState(ctx, v); err != nil {
			return err
		}
	}
	return nil
}

// EnsureContainer creates, recreates or starts a version's container.
func (m *Manager) EnsureContainer(ctx context.Context, ver string) (docker.EnsureResult, error) {
	return docker.Ensure(ctx, m.eng, Spec(m.p, ver), m.p.Root)
}

// --- files -------------------------------------------------------------------

// productionIni reads the image's php.ini-production.
func (m *Manager) productionIni(ctx context.Context, ver string) ([]byte, error) {
	name := ContainerName(ver) + "-ini"
	_ = m.eng.ContainerRemove(ctx, name, true)
	res, err := m.eng.RunOnce(ctx, name, docker.ContainerConfig{
		Image:      Image(ver),
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
