package sites

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
)

// PHPRuntime is what the sites model needs from PHP. *php.Manager implements
// it; tests substitute a fake.
type PHPRuntime interface {
	Paths() php.Paths
	// Usable reports whether sites can run on a version.
	Usable(ctx context.Context, ver string) error
	// Service is a version as a confedit service. With start set, its reload
	// brings the version up when it is not running.
	Service(ver string, start bool) confedit.Service
	// BaseFiles are the files a version's FPM starts on that are missing or
	// out of date.
	BaseFiles(ctx context.Context, ver string) ([]confedit.File, error)
	// EnsureRunning writes the version's unit and starts it when it is not
	// running.
	EnsureRunning(ctx context.Context, ver string) (php.EnsureResult, error)
}

// phpInputError turns "that version cannot be used" into a rejected field, so
// the form points at the version picker. Anything else passes through.
func phpInputError(field string, err error) error {
	switch {
	case errors.Is(err, php.ErrNotReady):
		return invalid(field, "that PHP version is not installed; pull it on the PHP page first")
	case errors.Is(err, php.ErrUnknownVersion):
		return invalid(field, "there is no such PHP version")
	}
	return err
}

// requirePHP returns the site's version, or a rejection for a site that is not
// a PHP site.
func (m *Manager) requirePHP(s Site) (string, error) {
	if s.Kind != KindPHP || s.PHPVersion == nil || s.PHPPool == "" {
		return "", invalid("kind", "%s is not a PHP site", s.Domain)
	}
	if m.php == nil {
		return "", invalid("kind", "PHP is not available on this host")
	}
	return *s.PHPVersion, nil
}

// prepareLog creates the site's PHP error log owned by www. The pool's workers
// write it after dropping privileges and cannot create a file in a directory
// that belongs to root.
func (m *Manager) prepareLog(domain string) error {
	name := m.php.Paths().ErrorLog(domain)
	if _, err := m.fs.Stat(name); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("sites: php log %s: %w", name, err)
	}
	if err := m.fs.WriteFile(name, nil, 0o640); err != nil {
		return fmt.Errorf("sites: create %s: %w", name, err)
	}
	if err := m.fs.Chown(name, nginx.WWWUID, nginx.WWWGID); err != nil {
		_ = m.fs.Remove(name)
		return fmt.Errorf("sites: give %s to %s: %w", name, nginx.WWWUser, err)
	}
	return nil
}

func (m *Manager) renderPHPInclude(s Site) ([]byte, error) {
	ver, err := m.requirePHP(s)
	if err != nil {
		return nil, err
	}
	return m.p.RenderPHP(m.php.Paths().Socket(ver, s.PHPPool))
}

// insertPool records a new site's pool. Two domains can map to one pool name;
// the second is refused rather than given the first one's socket.
func insertPool(ctx context.Context, tx *sql.Tx, domain, name string, s php.PoolSettings) error {
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT domain FROM php_pools WHERE name = ?`, name).Scan(&owner)
	if err == nil {
		return fmt.Errorf("%w: the PHP pool name %s (used by %s)", ErrConflict, name, owner)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("sites: check pool %s: %w", name, err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO php_pools (domain, name, json) VALUES (?, ?, ?)`, domain, name, string(raw)); err != nil {
		return fmt.Errorf("sites: insert pool %s: %w", name, err)
	}
	return nil
}

func (m *Manager) poolSettings(ctx context.Context, domain string) (php.PoolSettings, error) {
	var raw string
	err := m.st.DB().QueryRowContext(ctx, `SELECT json FROM php_pools WHERE domain = ?`, domain).Scan(&raw)
	if err != nil {
		return php.PoolSettings{}, fmt.Errorf("sites: read pool of %s: %w", domain, err)
	}
	s := php.DefaultPoolSettings()
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return php.PoolSettings{}, fmt.Errorf("sites: stored pool of %s is damaged: %w", domain, err)
	}
	return s, nil
}

func (m *Manager) savePool(ctx context.Context, domain string, s php.PoolSettings) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = m.st.DB().ExecContext(ctx, `UPDATE php_pools SET json = ? WHERE domain = ?`, string(raw), domain)
	return err
}

func (m *Manager) poolFile(s Site, ver string, ps php.PoolSettings) (confedit.File, error) {
	pp := m.php.Paths()
	b, err := pp.RenderPool(php.Pool{Name: s.PHPPool, Domain: s.Domain, Version: ver, Root: s.Root, Settings: ps})
	if err != nil {
		return confedit.File{}, err
	}
	return confedit.File{Path: pp.PoolFile(ver, s.PHPPool), Content: b}, nil
}

// phpFiles is what a PHP site needs on the FPM side: its pool file, and
// whatever the version's own files are missing.
func (m *Manager) phpFiles(ctx context.Context, s Site, ps php.PoolSettings) ([]confedit.File, error) {
	ver, err := m.requirePHP(s)
	if err != nil {
		return nil, err
	}
	pool, err := m.poolFile(s, ver, ps)
	if err != nil {
		return nil, err
	}
	base, err := m.php.BaseFiles(ctx, ver)
	if err != nil {
		return nil, err
	}
	return append([]confedit.File{pool}, base...), nil
}

// Pool is a PHP site's pool.
type Pool struct {
	Name     string           `json:"name"`
	Version  string           `json:"version"`
	Socket   string           `json:"socket"`
	File     string           `json:"file"`
	Settings php.PoolSettings `json:"settings"`
}

func (m *Manager) pool(ctx context.Context, s Site) (Pool, error) {
	ver, err := m.requirePHP(s)
	if err != nil {
		return Pool{}, err
	}
	ps, err := m.poolSettings(ctx, s.Domain)
	if err != nil {
		return Pool{}, err
	}
	pp := m.php.Paths()
	return Pool{Name: s.PHPPool, Version: ver, Socket: pp.Socket(ver, s.PHPPool), File: pp.PoolFile(ver, s.PHPPool), Settings: ps}, nil
}

// Pool returns a PHP site's pool.
func (m *Manager) Pool(ctx context.Context, domain string) (Pool, error) {
	s, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return Pool{}, err
	}
	return m.pool(ctx, s)
}

// SetPool changes a pool's process-manager settings: the database first, then
// the pool file through confedit with `php-fpm -t`, and the database put back
// if that fails.
func (m *Manager) SetPool(ctx context.Context, domain string, ps php.PoolSettings) (Pool, confedit.Result, error) {
	if err := ps.Validate(); err != nil {
		return Pool{}, confedit.Result{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return Pool{}, confedit.Result{}, err
	}
	prev, err := m.pool(ctx, s)
	if err != nil {
		return Pool{}, confedit.Result{}, err
	}
	files, err := m.phpFiles(ctx, s, ps)
	if err != nil {
		return Pool{}, confedit.Result{}, err
	}
	if err := m.savePool(ctx, s.Domain, ps); err != nil {
		return Pool{}, confedit.Result{}, fmt.Errorf("sites: store pool of %s: %w", s.Domain, err)
	}
	res, err := m.ap.Apply(ctx, confedit.Change{
		Files: files, Services: []confedit.Service{m.php.Service(prev.Version, true)}, Reason: "php pool " + s.Domain,
	})
	if err != nil {
		if rbErr := m.savePool(context.WithoutCancel(ctx), s.Domain, prev.Settings); rbErr != nil {
			err = errors.Join(err, fmt.Errorf("sites: restoring the pool of %s in the database: %w", s.Domain, rbErr))
		}
		return Pool{}, confedit.Result{}, err
	}
	prev.Settings = ps
	return prev, res, nil
}

func (m *Manager) storeVersion(ctx context.Context, domain, ver string) error {
	_, err := m.st.DB().ExecContext(ctx, `UPDATE sites SET php_version = ? WHERE domain = ?`, ver, domain)
	return err
}

// SwitchPHP moves a site to another PHP version in one apply of three files:
// the old pool removed, the new pool added, php.conf pointed at the new
// socket. The new FPM reloads first, then nginx, then the old FPM, so there is
// never a moment when nginx points at a socket nothing listens on. If any step
// fails every file is restored and the site keeps serving from the old version.
func (m *Manager) SwitchPHP(ctx context.Context, domain, ver string) (UpdateResult, error) {
	if php.CheckVersion(ver) != nil {
		return UpdateResult{}, invalid("php_version", "choose an installed PHP version")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	prev, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return UpdateResult{}, err
	}
	old, err := m.requirePHP(prev)
	if err != nil {
		return UpdateResult{}, err
	}
	if old == ver {
		return UpdateResult{}, invalid("php_version", "the site already runs on PHP %s", ver)
	}
	if err := m.php.Usable(ctx, ver); err != nil {
		return UpdateResult{}, phpInputError("php_version", err)
	}
	detail, err := m.scan(prev)
	if err != nil {
		return UpdateResult{}, err
	}
	if err := detail.require(FeaturePHP); err != nil {
		return UpdateResult{}, err
	}
	ps, err := m.poolSettings(ctx, prev.Domain)
	if err != nil {
		return UpdateResult{}, err
	}

	next := prev
	next.PHPVersion = &ver
	files, err := m.phpFiles(ctx, next, ps)
	if err != nil {
		return UpdateResult{}, err
	}
	inc, err := m.renderPHPInclude(next)
	if err != nil {
		return UpdateResult{}, err
	}
	patch, err := m.newPatch(prev)
	if err != nil {
		return UpdateResult{}, err
	}
	patch.set(FeaturePHP, inc)
	files = append(files, confedit.File{Path: m.php.Paths().PoolFile(old, prev.PHPPool), Delete: true})
	files = append(files, patch.files()...)

	if err := m.storeVersion(ctx, prev.Domain, ver); err != nil {
		return UpdateResult{}, fmt.Errorf("sites: update %s: %w", prev.Domain, err)
	}
	res, err := m.ap.Apply(ctx, confedit.Change{
		Files:    files,
		Services: []confedit.Service{m.php.Service(ver, true), m.svc, m.php.Service(old, false)},
		Reason:   fmt.Sprintf("switch %s from php %s to %s", prev.Domain, old, ver),
	})
	if err != nil {
		if rbErr := m.storeVersion(context.WithoutCancel(ctx), prev.Domain, old); rbErr != nil {
			err = errors.Join(err, fmt.Errorf("sites: restoring %s in the database: %w", prev.Domain, rbErr))
		}
		return UpdateResult{}, err
	}
	return UpdateResult{Site: next, Apply: res}, nil
}

// PHPStep is how one version came out of ReconcilePHP.
type PHPStep struct {
	Version string `json:"version"`
	Sites   int    `json:"sites"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
}

// ReconcilePHP extends the boot reconcile to PHP (step 2.7): for every version
// that has sites, the panel-owned files (php-fpm.conf, the form's ini, each
// site's pool) are rewritten where they differ, validated, and the container is
// brought up. A version with no site is left as it is: it starts with its first
// site and is never stopped here. php.ini is the operator's and is not touched.
func (m *Manager) ReconcilePHP(ctx context.Context) []PHPStep {
	if m.php == nil {
		return nil
	}
	sites, err := m.List(ctx)
	if err != nil {
		return []PHPStep{{Detail: err.Error()}}
	}
	byVer := map[string][]Site{}
	for _, s := range sites {
		if s.Kind == KindPHP && s.PHPVersion != nil {
			byVer[*s.PHPVersion] = append(byVer[*s.PHPVersion], s)
		}
	}
	vers := make([]string, 0, len(byVer))
	for v := range byVer {
		vers = append(vers, v)
	}
	sort.Strings(vers)

	var out []PHPStep
	for _, ver := range vers {
		step := PHPStep{Version: ver, Sites: len(byVer[ver])}
		detail, err := m.reconcileVersion(ctx, ver, byVer[ver])
		step.OK, step.Detail = err == nil, detail
		if err != nil {
			step.Detail = err.Error()
		}
		out = append(out, step)
	}
	return out
}

func (m *Manager) reconcileVersion(ctx context.Context, ver string, sites []Site) (string, error) {
	if err := m.php.Usable(ctx, ver); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	files, err := m.php.BaseFiles(ctx, ver)
	if err != nil {
		return "", err
	}
	for _, s := range sites {
		ps, err := m.poolSettings(ctx, s.Domain)
		if err != nil {
			return "", err
		}
		f, err := m.poolFile(s, ver, ps)
		if err != nil {
			return "", err
		}
		have, err := m.fs.ReadFile(f.Path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("read %s: %w", f.Path, err)
		}
		if err != nil || !bytes.Equal(have, f.Content) {
			files = append(files, f)
		}
		_ = m.prepareLog(s.Domain)
	}
	if len(files) > 0 {
		if _, err := m.ap.Apply(ctx, confedit.Change{
			Files: files, Services: []confedit.Service{m.php.Service(ver, true)}, Reason: "boot reconcile php " + ver,
		}); err != nil {
			return "", err
		}
		return fmt.Sprintf("%d file(s) written and validated", len(files)), nil
	}
	ens, err := m.php.EnsureRunning(ctx, ver)
	if err != nil {
		return "", err
	}
	return string(ens.Action) + ": " + ens.Reason, nil
}
