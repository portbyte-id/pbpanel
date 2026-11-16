package sites

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/store"
)

// Errors a caller maps to a status.
var (
	ErrNotFound = errors.New("sites: no such site")
	ErrConflict = errors.New("sites: the domain is already in use")
	// ErrDetached is matched by DetachedError: the control the caller wanted is
	// switched off because the site file no longer includes its file.
	ErrDetached = errors.New("sites: the feature is detached from the site file")
)

// Kind aliases the nginx type so callers import one package for sites.
type Kind = nginx.Kind

// Site kinds.
const (
	KindStatic = nginx.KindStatic
	KindProxy  = nginx.KindProxy
)

const placeholderIndex = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>%s</title></head>
<body><h1>%s</h1><p>This site is served by PB panel. Replace this file with your own.</p></body>
</html>
`

// Site is one website.
type Site struct {
	Domain      string    `json:"domain"`
	Aliases     []string  `json:"aliases"`
	Kind        Kind      `json:"kind"`
	Root        string    `json:"root,omitempty"`
	ProxyTarget string    `json:"proxy_target,omitempty"`
	PHPVersion  *string   `json:"php_version"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s Site) nginx() nginx.Site {
	return nginx.Site{Domain: s.Domain, Aliases: s.Aliases, Kind: s.Kind, Root: s.Root, ProxyTarget: s.ProxyTarget}
}

// FS is the filesystem the manager touches outside confedit: web roots, and the
// site file when scanning it. It exists so the model is tested without a root
// filesystem or the www account.
type FS interface {
	ReadFile(name string) ([]byte, error)
	Stat(name string) (fs.FileInfo, error)
	MkdirAll(name string, perm fs.FileMode) error
	WriteFile(name string, data []byte, perm fs.FileMode) error
	Remove(name string) error
	Chown(name string, uid, gid int) error
}

// OSFS is the real filesystem.
type OSFS struct{}

// ReadFile implements FS.
func (OSFS) ReadFile(n string) ([]byte, error) { return os.ReadFile(n) }

// Stat implements FS.
func (OSFS) Stat(n string) (fs.FileInfo, error) { return os.Stat(n) }

// MkdirAll implements FS.
func (OSFS) MkdirAll(n string, p fs.FileMode) error { return os.MkdirAll(n, p) }

// WriteFile implements FS.
func (OSFS) WriteFile(n string, d []byte, p fs.FileMode) error { return os.WriteFile(n, d, p) }

// Remove implements FS.
func (OSFS) Remove(n string) error { return os.Remove(n) }

// Chown implements FS.
func (OSFS) Chown(n string, uid, gid int) error { return os.Chown(n, uid, gid) }

// Options configures a Manager.
type Options struct {
	Store   *store.Store
	Applier nginx.Applier
	// Service is the live nginx service: every site change validates and reloads
	// through it.
	Service confedit.Service
	Paths   nginx.Paths
	// FS defaults to the real filesystem.
	FS  FS
	Now func() time.Time
}

// Manager owns the sites model. It is safe for concurrent use; changes run one
// at a time.
type Manager struct {
	st  *store.Store
	ap  nginx.Applier
	svc confedit.Service
	p   nginx.Paths
	fs  FS
	now func() time.Time

	mu sync.Mutex
}

// New returns a Manager.
func New(o Options) (*Manager, error) {
	if o.Store == nil || o.Applier == nil || o.Service == nil {
		return nil, errors.New("sites: store, applier and service are required")
	}
	m := &Manager{st: o.Store, ap: o.Applier, svc: o.Service, p: o.Paths, fs: o.FS, now: o.Now}
	if m.fs == nil {
		m.fs = OSFS{}
	}
	if m.now == nil {
		m.now = time.Now
	}
	return m, nil
}

// CreateInput is a new site.
type CreateInput struct {
	Domain  string
	Aliases []string
	Kind    Kind
	// Root is the web root of a static site; empty means /www/services/sites/<domain>.
	Root        string
	ProxyTarget string
}

// CreateResult is what Create did.
type CreateResult struct {
	Site  Site
	Apply confedit.Result
}

// Create adds a site: the database row, the web root with a placeholder page,
// then the config through confedit. If the apply fails the row and anything
// created for it are removed, and the validator's output is in the error.
func (m *Manager) Create(ctx context.Context, in CreateInput) (CreateResult, error) {
	site, err := m.checkCreate(in)
	if err != nil {
		return CreateResult{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	site.CreatedAt = m.now().UTC()
	if err := m.insert(ctx, site); err != nil {
		return CreateResult{}, err
	}

	var created bool
	if site.Kind == KindStatic {
		if created, err = m.prepareRoot(site); err != nil {
			_ = m.deleteRow(context.WithoutCancel(ctx), site.Domain)
			return CreateResult{}, err
		}
	}

	files, err := m.allFiles(site)
	if err == nil {
		var res confedit.Result
		res, err = m.ap.Apply(ctx, confedit.Change{Files: files, Services: []confedit.Service{m.svc}, Reason: "create site " + site.Domain})
		if err == nil {
			return CreateResult{Site: site, Apply: res}, nil
		}
	}

	// Undo in reverse: the config is already restored by the apply.
	bg := context.WithoutCancel(ctx)
	if rbErr := m.deleteRow(bg, site.Domain); rbErr != nil {
		err = errors.Join(err, fmt.Errorf("sites: removing the new database row: %w", rbErr))
	}
	if created {
		// Not RemoveAll: whatever the operator may have put there since is theirs.
		_ = m.fs.Remove(path.Join(site.Root, "index.html"))
		_ = m.fs.Remove(site.Root)
	}
	return CreateResult{}, err
}

func (m *Manager) checkCreate(in CreateInput) (Site, error) {
	domain, err := NormalizeDomain("domain", in.Domain)
	if err != nil {
		return Site{}, err
	}
	aliases, err := normalizeAliases(domain, in.Aliases)
	if err != nil {
		return Site{}, err
	}
	site := Site{Domain: domain, Aliases: aliases, Kind: in.Kind}
	switch in.Kind {
	case KindStatic:
		if in.ProxyTarget != "" {
			return Site{}, invalid("proxy_target", "is only for a reverse-proxy site")
		}
		site.Root = in.Root
		if site.Root == "" {
			site.Root = m.p.SiteRoot(domain)
		}
		if err := m.checkRoot(site.Root); err != nil {
			return Site{}, err
		}
	case KindProxy:
		if in.Root != "" {
			return Site{}, invalid("root", "is only for a static site")
		}
		t, err := nginx.CheckProxyTarget(in.ProxyTarget)
		if err != nil {
			return Site{}, invalid("proxy_target", "%s", strings.TrimPrefix(err.Error(), "nginx: "))
		}
		site.ProxyTarget = t
	default:
		return Site{}, invalid("kind", "must be static or proxy")
	}
	return site, nil
}

// checkRoot keeps a web root where the container can see it: below
// /www/services/sites, as a clean path, never the directory itself.
func (m *Manager) checkRoot(root string) error {
	if !path.IsAbs(root) || path.Clean(root) != root || strings.ContainsAny(root, " \t\r\n;{}$\"'\\#\x00") {
		return invalid("root", "%q must be a clean absolute path without spaces or special characters", root)
	}
	if !strings.HasPrefix(root, m.p.SitesRoot()+"/") {
		return invalid("root", "must be inside %s: nginx cannot see other directories", m.p.SitesRoot())
	}
	return nil
}

// prepareRoot creates the web root and its placeholder page, owned by www. It
// reports whether it created the directory, which decides whether a failed
// create removes it again. An existing directory is left exactly as it is.
func (m *Manager) prepareRoot(s Site) (created bool, err error) {
	if _, err := m.fs.Stat(s.Root); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("sites: web root %s: %w", s.Root, err)
	}
	if err := m.fs.MkdirAll(s.Root, 0o755); err != nil {
		return false, fmt.Errorf("sites: create web root %s: %w", s.Root, err)
	}
	index := path.Join(s.Root, "index.html")
	body := fmt.Appendf(nil, placeholderIndex, s.Domain, s.Domain)
	if err := m.fs.WriteFile(index, body, 0o644); err != nil {
		_ = m.fs.Remove(s.Root)
		return false, fmt.Errorf("sites: write %s: %w", index, err)
	}
	for _, p := range []string{s.Root, index} {
		if err := m.fs.Chown(p, install.WWWUID, install.WWWGID); err != nil {
			_ = m.fs.Remove(index)
			_ = m.fs.Remove(s.Root)
			return false, fmt.Errorf("sites: give %s to %s: %w", p, install.WWWUser, err)
		}
	}
	return true, nil
}

// includeFiles renders the panel-owned include files of a site.
func (m *Manager) includeFiles(s Site) ([]confedit.File, error) {
	listen, err := nginx.RenderListen()
	if err != nil {
		return nil, err
	}
	names, err := nginx.RenderNames(s.Domain, s.Aliases)
	if err != nil {
		return nil, err
	}
	acme, err := nginx.RenderACME()
	if err != nil {
		return nil, err
	}
	files := []confedit.File{
		{Path: m.p.SiteInclude(s.Domain, nginx.IncludeListen), Content: listen},
		{Path: m.p.SiteInclude(s.Domain, nginx.IncludeNames), Content: names},
		{Path: m.p.SiteInclude(s.Domain, nginx.IncludeACME), Content: acme},
	}
	if s.Kind == KindProxy {
		proxy, err := nginx.RenderProxy(s.ProxyTarget)
		if err != nil {
			return nil, err
		}
		files = append(files, confedit.File{Path: m.p.SiteInclude(s.Domain, nginx.IncludeProxy), Content: proxy})
	}
	return files, nil
}

func (m *Manager) siteFile(s Site) (confedit.File, error) {
	b, err := m.p.RenderSiteFile(s.nginx())
	if err != nil {
		return confedit.File{}, err
	}
	return confedit.File{Path: m.p.SiteFile(s.Domain), Content: b}, nil
}

// allFiles is every file a site owns: the include files and the site file.
func (m *Manager) allFiles(s Site) ([]confedit.File, error) {
	files, err := m.includeFiles(s)
	if err != nil {
		return nil, err
	}
	sf, err := m.siteFile(s)
	if err != nil {
		return nil, err
	}
	return append(files, sf), nil
}

// DeleteResult says what Delete left behind.
type DeleteResult struct {
	Apply confedit.Result
	// WebRoot is the static site's web root, which is kept: moving it to the
	// trash arrives with M3.
	WebRoot string
}

// Delete removes the site's config through confedit, then its database row. The
// web root stays on disk and the result says so.
func (m *Manager) Delete(ctx context.Context, domain string) (DeleteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return DeleteResult{}, err
	}
	files := []confedit.File{{Path: m.p.SiteFile(s.Domain), Delete: true}}
	for _, name := range []string{nginx.IncludeListen, nginx.IncludeNames, nginx.IncludeACME, nginx.IncludeProxy} {
		files = append(files, confedit.File{Path: m.p.SiteInclude(s.Domain, name), Delete: true})
	}
	res, err := m.ap.Apply(ctx, confedit.Change{Files: files, Services: []confedit.Service{m.svc}, Reason: "delete site " + s.Domain})
	if err != nil {
		return DeleteResult{}, err
	}
	// Best effort: the directory is empty now, and an empty one is harmless.
	_ = m.fs.Remove(m.p.SiteIncludesDir(s.Domain))

	if err := m.deleteRow(ctx, s.Domain); err != nil {
		return DeleteResult{Apply: res}, fmt.Errorf("sites: the configuration of %s was removed but its database row was not: %w", s.Domain, err)
	}
	out := DeleteResult{Apply: res}
	if s.Kind == KindStatic {
		out.WebRoot = s.Root
	}
	return out, nil
}

// List returns every site, ordered by domain.
func (m *Manager) List(ctx context.Context) ([]Site, error) {
	return m.list(ctx, m.st.DB())
}

// Get returns one site.
func (m *Manager) Get(ctx context.Context, domain string) (Site, error) {
	return m.get(ctx, m.st.DB(), domain)
}

type queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func (m *Manager) list(ctx context.Context, q queryer) ([]Site, error) {
	rows, err := q.QueryContext(ctx, `SELECT domain, kind, root, proxy_target, php_version, created_at FROM sites ORDER BY domain`)
	if err != nil {
		return nil, fmt.Errorf("sites: list: %w", err)
	}
	var out []Site
	for rows.Next() {
		s, err := scanSite(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("sites: list: %w", err)
	}
	_ = rows.Close()

	for i := range out {
		if out[i].Aliases, err = aliasesOf(ctx, q, out[i].Domain); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (m *Manager) get(ctx context.Context, q queryer, domain string) (Site, error) {
	d, err := NormalizeDomain("domain", domain)
	if err != nil {
		return Site{}, fmt.Errorf("%w: %s", ErrNotFound, domain)
	}
	s, err := scanSite(q.QueryRowContext(ctx,
		`SELECT domain, kind, root, proxy_target, php_version, created_at FROM sites WHERE domain = ?`, d))
	if errors.Is(err, sql.ErrNoRows) {
		return Site{}, fmt.Errorf("%w: %s", ErrNotFound, d)
	}
	if err != nil {
		return Site{}, err
	}
	s.Aliases, err = aliasesOf(ctx, q, s.Domain)
	return s, err
}

type scanner interface{ Scan(dest ...any) error }

func scanSite(r scanner) (Site, error) {
	var s Site
	var php sql.NullString
	var created string
	if err := r.Scan(&s.Domain, &s.Kind, &s.Root, &s.ProxyTarget, &php, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Site{}, err
		}
		return Site{}, fmt.Errorf("sites: read row: %w", err)
	}
	if php.Valid {
		s.PHPVersion = &php.String
	}
	t, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Site{}, fmt.Errorf("sites: %s has a damaged creation time: %w", s.Domain, err)
	}
	s.CreatedAt = t
	return s, nil
}

func aliasesOf(ctx context.Context, q queryer, domain string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT alias FROM site_aliases WHERE domain = ? ORDER BY position`, domain)
	if err != nil {
		return nil, fmt.Errorf("sites: aliases of %s: %w", domain, err)
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, fmt.Errorf("sites: aliases of %s: %w", domain, err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// claimed returns the first of names already used as a domain or alias by a
// site other than except.
func claimed(ctx context.Context, tx *sql.Tx, names []string, except string) (string, bool, error) {
	for _, n := range names {
		var owner string
		err := tx.QueryRowContext(ctx, `
			SELECT domain FROM sites WHERE domain = ?1 AND domain <> ?2
			UNION
			SELECT domain FROM site_aliases WHERE alias = ?1 AND domain <> ?2`, n, except).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", false, fmt.Errorf("sites: check %s: %w", n, err)
		}
		return n + " (used by " + owner + ")", true, nil
	}
	return "", false, nil
}

func (m *Manager) insert(ctx context.Context, s Site) error {
	return m.st.WithTx(ctx, func(tx *sql.Tx) error {
		if name, taken, err := claimed(ctx, tx, append([]string{s.Domain}, s.Aliases...), ""); err != nil {
			return err
		} else if taken {
			return fmt.Errorf("%w: %s", ErrConflict, name)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sites (domain, kind, root, proxy_target, php_version, created_at) VALUES (?, ?, ?, ?, NULL, ?)`,
			s.Domain, string(s.Kind), s.Root, s.ProxyTarget, s.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("sites: insert %s: %w", s.Domain, err)
		}
		return setAliases(ctx, tx, s.Domain, s.Aliases)
	})
}

func setAliases(ctx context.Context, tx *sql.Tx, domain string, aliases []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_aliases WHERE domain = ?`, domain); err != nil {
		return fmt.Errorf("sites: clear aliases of %s: %w", domain, err)
	}
	for i, a := range aliases {
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_aliases (alias, domain, position) VALUES (?, ?, ?)`, a, domain, i); err != nil {
			return fmt.Errorf("sites: add alias %s: %w", a, err)
		}
	}
	return nil
}

// deleteRow removes a site and its aliases. Foreign keys are off in this
// database, so the cascade in the schema is not relied on.
func (m *Manager) deleteRow(ctx context.Context, domain string) error {
	return m.st.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM site_aliases WHERE domain = ?`, domain); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sites WHERE domain = ?`, domain)
		return err
	})
}
