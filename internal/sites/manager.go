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
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/waf"
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
	KindPHP    = nginx.KindPHP
)

const placeholderIndex = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>%s</title></head>
<body><h1>%s</h1><p>This site is served by PB panel. Replace this file with your own.</p></body>
</html>
`

// Site is one website.
//
// Kind is the site's primary serving mode: where its content lives and how a
// request that no proxy route claims is answered. It does not decide the feature
// set. A site may serve files from a folder, run PHP, and proxy one or more
// subpaths, all at once; what is rendered follows what the site actually has.
type Site struct {
	Domain  string   `json:"domain"`
	Aliases []string `json:"aliases"`
	Kind    Kind     `json:"kind"`
	Root    string   `json:"root,omitempty"`
	// ProxyRoutes are the subpath routes, in the order the site lists them.
	ProxyRoutes []ProxyRoute `json:"proxy_routes"`
	// ProxyTarget repeats the target of the "/" route, for the API clients that
	// predate proxy routes (the Apps page and the old create form). It is derived
	// on every read and never stored on its own; `proxy_routes` is the truth.
	ProxyTarget string  `json:"proxy_target,omitempty"`
	PHPVersion  *string `json:"php_version"`
	// PHPPool is the FPM pool name of a PHP site.
	PHPPool string `json:"php_pool,omitempty"`
	// Rewrite is the URL-rewrite preset of a PHP site: "" (the generic default),
	// RewriteLaravel or RewriteWordPress.
	Rewrite   Rewrite   `json:"rewrite"`
	CreatedAt time.Time `json:"created_at"`
}

// ProxyRoute sends one path prefix of a site to a backend. Path is normalised by
// nginx.CheckProxyPath ("/", "/api", "/api/v2"); Target by
// nginx.CheckProxyTarget. StripPrefix decides whether the matched prefix reaches
// the backend: with it, /api/users arrives as /users; without it, as /api/users.
type ProxyRoute = nginx.ProxyRoute

// Rewrite names a front-controller preset that decides the php.conf `location /`
// line. The zero value is the panel's generic default.
type Rewrite string

// The rewrite presets offered in the site dialog.
const (
	RewriteNone      Rewrite = ""
	RewriteLaravel   Rewrite = "laravel"
	RewriteWordPress Rewrite = "wordpress"
)

// FrontControllerTail is the `try_files` argument a preset passes a missing file
// to. It is what differs between Laravel and WordPress.
func (r Rewrite) FrontControllerTail() string {
	switch r {
	case RewriteLaravel:
		return "/index.php?$query_string"
	case RewriteWordPress:
		return "/index.php?$args"
	default:
		return "/index.php?$query_string"
	}
}

// ValidRewrite reports whether r is a preset this build knows.
func ValidRewrite(r Rewrite) bool {
	switch r {
	case RewriteNone, RewriteLaravel, RewriteWordPress:
		return true
	}
	return false
}

func (s Site) nginx() nginx.Site {
	return nginx.Site{Domain: s.Domain, Aliases: s.Aliases, Kind: s.Kind, Root: s.Root, ProxyRoutes: s.ProxyRoutes, HasPHP: s.PHPVersion != nil}
}

// TLSInfo is a site's certificate as the nginx config needs it.
type TLSInfo struct {
	// CertFile is the full chain and KeyFile the private key. Both must be
	// inside nginx.Paths.SiteCertDir for the site.
	CertFile, KeyFile string
	// RedirectHTTP sends plain-HTTP requests, except ACME challenges, to HTTPS.
	RedirectHTTP bool
}

// TLSState is the certificate store as seen from the sites model. ok is false
// for a site that has no certificate.
type TLSState interface {
	Lookup(ctx context.Context, domain string) (info TLSInfo, ok bool, err error)
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
	// PHP is the PHP runtime. Nil leaves the `php` site kind refused.
	PHP PHPRuntime
	// TLS says which sites have a certificate. Nil means none do: every site
	// gets an empty tls.conf, as before TLS existed.
	TLS TLSState
	// FS defaults to the real filesystem.
	FS  FS
	Now func() time.Time
	// WAF is the per-site firewall. Nil leaves every site's firewall block empty
	// and the firewall routes answering 503.
	WAF *waf.Engine
	// Appsec is the open-appsec agent as a config service. Nil means the engine
	// cannot be used: a change that asks for it is refused before anything is
	// written, rather than loading a module whose agent is not there.
	//
	// When it is set, every change to an open-appsec site lists it after nginx,
	// so an apply writes the nginx files and the policy, validates both, reloads
	// nginx, then hands the policy to the agent — and a failure at any point
	// rolls all of it back.
	Appsec confedit.Service
	// Settings renders panel-main.conf and panel-http.conf. The first firewall
	// change on a host that predates the firewall uses it to bring them current,
	// because they carry the include lines that read the firewall's files.
	Settings func(ctx context.Context) ([]confedit.File, error)
}

// Manager owns the sites model. It is safe for concurrent use; changes run one
// at a time.
type Manager struct {
	st  *store.Store
	ap  nginx.Applier
	svc confedit.Service
	p   nginx.Paths
	php PHPRuntime
	tls TLSState
	fs  FS
	now func() time.Time
	waf *waf.Engine
	// appsec is Options.Appsec, the open-appsec agent as a config service.
	appsec confedit.Service
	// settings is Options.Settings.
	settings func(ctx context.Context) ([]confedit.File, error)

	mu sync.Mutex
}

// New returns a Manager.
func New(o Options) (*Manager, error) {
	if o.Store == nil || o.Applier == nil || o.Service == nil {
		return nil, errors.New("sites: store, applier and service are required")
	}
	m := &Manager{st: o.Store, ap: o.Applier, svc: o.Service, p: o.Paths, php: o.PHP, tls: o.TLS, fs: o.FS, now: o.Now, waf: o.WAF, appsec: o.Appsec, settings: o.Settings}
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
	// Root is the web root; empty means /www/services/sites/<domain>. A proxy
	// site has none.
	Root string
	// ProxyRoutes are the subpath routes. ProxyTarget is the single-root form the
	// old create form sends: it becomes the "/" route and is refused alongside
	// ProxyRoutes, since two spellings of one route would be ambiguous.
	ProxyRoutes []ProxyRoute
	ProxyTarget string
	// PHPVersion is the version a PHP site runs on. Empty means no PHP, which is
	// what a static or proxy site has.
	PHPVersion string
	// PHPInfo puts an index.php that prints phpinfo() in a new web root. It is
	// off by default: that page tells a visitor everything about the server.
	PHPInfo bool
	// Rewrite is the URL-rewrite preset of a PHP site.
	Rewrite Rewrite
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

	if site.PHPVersion != nil {
		if err := m.php.Usable(ctx, *site.PHPVersion); err != nil {
			return CreateResult{}, phpInputError("php_version", err)
		}
	}

	site.CreatedAt = m.now().UTC()
	if err := m.insert(ctx, site); err != nil {
		return CreateResult{}, err
	}

	// A site that serves files from a folder — static or PHP — gets the folder.
	// A pure proxy site has none.
	var created bool
	index := "index.html"
	if site.PHPVersion != nil && in.PHPInfo {
		index = "index.php"
	}
	if site.Root != "" {
		created, err = m.prepareRoot(site, index)
		if err == nil && site.PHPVersion != nil {
			err = m.prepareLog(site.Domain)
		}
		if err != nil {
			_ = m.deleteRow(context.WithoutCancel(ctx), site.Domain)
			return CreateResult{}, err
		}
	}

	files, err := m.allFiles(ctx, site)
	services := []confedit.Service{m.svc}
	if err == nil && site.PHPVersion != nil {
		// FPM first: nginx must not be pointed at a socket nothing listens on.
		var more []confedit.File
		if more, err = m.phpFiles(ctx, site, php.DefaultPoolSettings()); err == nil {
			files = append(files, more...)
			services = []confedit.Service{m.php.Service(*site.PHPVersion, true), m.svc}
		}
	}
	if err == nil {
		var res confedit.Result
		res, err = m.ap.Apply(ctx, confedit.Change{Files: files, Services: services, Reason: "create site " + site.Domain})
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
		_ = m.fs.Remove(path.Join(site.Root, index))
		_ = m.fs.Remove(site.Root)
	}
	return CreateResult{}, err
}

// checkCreate validates a new site and returns the row to store: the kind, the
// folder, the PHP set, and the proxy routes, each validated on its own and then
// against each other (no duplicate path, no "/" next to PHP or a web root).
func (m *Manager) checkCreate(in CreateInput) (Site, error) {
	domain, err := NormalizeDomain("domain", in.Domain)
	if err != nil {
		return Site{}, err
	}
	aliases, err := normalizeAliases(domain, in.Aliases)
	if err != nil {
		return Site{}, err
	}
	site := Site{Domain: domain, Aliases: aliases, Kind: in.Kind, ProxyRoutes: []ProxyRoute{}}
	switch in.Kind {
	case KindStatic, KindProxy, KindPHP:
	default:
		return Site{}, invalid("kind", "must be static, proxy or php")
	}

	// The folder: a static or PHP site has one (the default when none is given),
	// a pure proxy site has none.
	switch in.Kind {
	case KindProxy:
		if in.Root != "" {
			return Site{}, invalid("root", "is only for a site that serves files")
		}
	default:
		site.Root = in.Root
		if site.Root == "" {
			site.Root = m.p.SiteRoot(domain)
		}
		if err := m.checkRoot(site.Root); err != nil {
			return Site{}, err
		}
	}

	// PHP, which no longer needs kind=php: a site with a version runs PHP.
	if in.PHPVersion == "" && in.Kind == KindPHP {
		return Site{}, invalid("php_version", "choose an installed PHP version")
	}
	if in.PHPVersion != "" {
		if m.php == nil {
			return Site{}, invalid("php_version", "PHP is not available on this host")
		}
		if php.CheckVersion(in.PHPVersion) != nil {
			return Site{}, invalid("php_version", "choose an installed PHP version")
		}
		if in.Kind == KindProxy {
			return Site{}, invalid("php_version", "a site that serves files is required for PHP; a pure proxy site has no folder")
		}
		if site.Root == "" {
			site.Root = m.p.SiteRoot(domain)
			if err := m.checkRoot(site.Root); err != nil {
				return Site{}, err
			}
		}
		v := in.PHPVersion
		site.PHPVersion, site.PHPPool = &v, php.PoolName(domain)
		// A site that runs PHP is primarily a PHP site: that is the mode its
		// folder is served in, and it is what the list shows.
		if site.Kind == KindStatic {
			site.Kind = KindPHP
		}
	}
	if in.PHPInfo && site.PHPVersion == nil {
		return Site{}, invalid("php_version", "choose a PHP version to get a phpinfo() page")
	}
	if !ValidRewrite(in.Rewrite) {
		return Site{}, invalid("rewrite", "choose one of the URL-rewrite presets")
	}
	if in.Rewrite != RewriteNone && site.PHPVersion == nil {
		return Site{}, invalid("rewrite", "is only for a site that runs PHP")
	}
	site.Rewrite = in.Rewrite

	// Proxy routes: the new list, or the old single root target.
	routes := in.ProxyRoutes
	if in.ProxyTarget != "" {
		if len(routes) > 0 {
			return Site{}, invalid("proxy_routes", "give either proxy_target or proxy_routes, not both")
		}
		routes = []ProxyRoute{{Path: "/", Target: in.ProxyTarget}}
	}
	if site.ProxyRoutes, err = m.checkRoutes(routes, site); err != nil {
		return Site{}, err
	}
	// A proxy site has no folder, so its routes are all it serves: one with none
	// would answer nothing.
	if site.Kind == KindProxy && len(site.ProxyRoutes) == 0 {
		return Site{}, invalid("proxy_routes", "a reverse-proxy site needs at least one route")
	}
	// Every read derives this field from the "/" route.
	for _, r := range site.ProxyRoutes {
		if r.Path == "/" {
			site.ProxyTarget = r.Target
		}
	}
	return site, nil
}

// checkRoutes normalises and validates a site's proxy routes against the site
// they belong to. Each path and target is checked on its own, duplicates are
// refused, and a route on "/" is refused when something else already claims
// `location /`: the PHP front controller, or a static site's own location.
func (m *Manager) checkRoutes(in []ProxyRoute, s Site) ([]ProxyRoute, error) {
	out := make([]ProxyRoute, 0, len(in))
	for _, r := range in {
		p, err := nginx.CheckProxyPath(r.Path)
		if err != nil {
			return nil, invalid("proxy_routes", "%s", trimPrefix(err))
		}
		t, err := nginx.CheckProxyTarget(r.Target)
		if err != nil {
			return nil, invalid("proxy_routes", "%s: %s", p, trimPrefix(err))
		}
		out = append(out, ProxyRoute{Path: p, Target: t, StripPrefix: r.StripPrefix})
	}
	if err := nginx.CheckProxyRoutes(out); err != nil {
		return nil, invalid("proxy_routes", "%s", trimPrefix(err))
	}
	for _, r := range out {
		if r.Path != "/" {
			continue
		}
		switch {
		case s.PHPVersion != nil:
			return nil, invalid("proxy_routes", `/ cannot be proxied while the site runs PHP: both would claim "location /". Proxy a subpath such as /api instead`)
		case s.Root != "":
			return nil, invalid("proxy_routes", `/ cannot be proxied while the site serves files from %s: both would claim "location /". Proxy a subpath such as /api instead`, s.Root)
		}
	}
	return out, nil
}

// checkRoot keeps a web root where PHP can be given it: below
// /www/services/sites, as a clean path, never the directory itself.
func (m *Manager) checkRoot(root string) error {
	if !path.IsAbs(root) || path.Clean(root) != root || strings.ContainsAny(root, " \t\r\n;{}$\"'\\#\x00") {
		return invalid("root", "%q must be a clean absolute path without spaces or special characters", root)
	}
	if !m.p.ServesRoot(root) {
		return invalid("root", "must be inside %s or an allowed web root: nginx cannot see other directories", m.p.SitesRoot())
	}
	return nil
}

// prepareRoot creates the web root and its placeholder page, owned by www. It
// reports whether it created the directory, which decides whether a failed
// create removes it again. An existing directory is left exactly as it is.
func (m *Manager) prepareRoot(s Site, indexName string) (created bool, err error) {
	if _, err := m.fs.Stat(s.Root); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("sites: web root %s: %w", s.Root, err)
	}
	if err := m.fs.MkdirAll(s.Root, 0o755); err != nil {
		return false, fmt.Errorf("sites: create web root %s: %w", s.Root, err)
	}
	index := path.Join(s.Root, indexName)
	body := fmt.Appendf(nil, placeholderIndex, s.Domain, s.Domain)
	if indexName == "index.php" {
		body = []byte("<?php phpinfo();\n")
	}
	if err := m.fs.WriteFile(index, body, 0o644); err != nil {
		_ = m.fs.Remove(s.Root)
		return false, fmt.Errorf("sites: write %s: %w", index, err)
	}
	for _, p := range []string{s.Root, index} {
		if err := m.fs.Chown(p, nginx.WWWUID, nginx.WWWGID); err != nil {
			_ = m.fs.Remove(index)
			_ = m.fs.Remove(s.Root)
			return false, fmt.Errorf("sites: give %s to %s: %w", p, nginx.WWWUser, err)
		}
	}
	return true, nil
}

// includeFiles renders the include files a site still has: the reverse proxy's,
// one file whatever the kind. A site with no route gets a comment-only file, so
// the site file's include line never dangles.
func (m *Manager) includeFiles(s Site) ([]confedit.File, error) {
	proxy, err := nginx.RenderProxy(s.ProxyRoutes)
	if err != nil {
		return nil, err
	}
	return []confedit.File{{Path: m.p.SiteInclude(s.Domain, nginx.IncludeProxy), Content: proxy}}, nil
}

// tlsBody renders the TLS block: the HTTPS listener when the store has a
// certificate for the site, a comment when it has none.
func (m *Manager) tlsBody(ctx context.Context, s Site) ([]byte, error) {
	var t *nginx.TLS
	if m.tls != nil {
		info, ok, err := m.tls.Lookup(ctx, s.Domain)
		if err != nil {
			return nil, fmt.Errorf("sites: look up the certificate of %s: %w", s.Domain, err)
		}
		if ok {
			t = &nginx.TLS{CertFile: info.CertFile, KeyFile: info.KeyFile, RedirectHTTP: info.RedirectHTTP}
		}
	}
	return m.p.RenderTLS(s.Domain, t)
}

// siteFile renders the whole site file with every managed block filled in.
func (m *Manager) siteFile(ctx context.Context, s Site) (confedit.File, error) {
	b, err := m.p.RenderSiteFile(s.nginx())
	if err != nil {
		return confedit.File{}, err
	}
	listen, err := nginx.RenderListen()
	if err != nil {
		return confedit.File{}, err
	}
	names, err := nginx.RenderNames(s.Domain, s.Aliases)
	if err != nil {
		return confedit.File{}, err
	}
	acme, err := m.p.RenderACME()
	if err != nil {
		return confedit.File{}, err
	}
	tls, err := m.tlsBody(ctx, s)
	if err != nil {
		return confedit.File{}, err
	}
	type part struct {
		name string
		body []byte
	}
	parts := []part{{nginx.BlockListen, listen}, {nginx.BlockNames, names}, {nginx.BlockACME, acme}, {nginx.BlockTLS, tls}}
	if rendered, ok, err := m.wafSiteFiles(ctx, s.Domain); err != nil {
		return confedit.File{}, err
	} else if ok {
		parts = append(parts, part{nginx.BlockWAF, rendered.Block})
	}
	if s.PHPVersion != nil {
		phpBody, err := m.renderPHPInclude(s)
		if err != nil {
			return confedit.File{}, err
		}
		parts = append(parts, part{nginx.BlockPHP, phpBody})
	}
	for _, pt := range parts {
		var ok bool
		if b, ok = nginx.ReplaceBlock(b, pt.name, pt.body); !ok {
			return confedit.File{}, fmt.Errorf("sites: the site template has no %s block", pt.name)
		}
	}
	return confedit.File{Path: m.p.SiteFile(s.Domain), Content: b}, nil
}

// allFiles is every file a site owns: the site file and the proxy include.
func (m *Manager) allFiles(ctx context.Context, s Site) ([]confedit.File, error) {
	files, err := m.includeFiles(s)
	if err != nil {
		return nil, err
	}
	sf, err := m.siteFile(ctx, s)
	if err != nil {
		return nil, err
	}
	files = append(files, sf)
	if rendered, ok, err := m.wafSiteFiles(ctx, s.Domain); err != nil {
		return nil, err
	} else if ok {
		files = append(files, rendered.Files...)
	}
	return files, nil
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
	for _, name := range []string{nginx.IncludeListen, nginx.IncludeNames, nginx.IncludeACME, nginx.IncludeProxy, nginx.IncludePHP} {
		files = append(files, confedit.File{Path: m.p.SiteInclude(s.Domain, name), Delete: true})
	}
	if m.waf != nil {
		files = append(files, m.waf.RemovedFiles(s.Domain)...)
		// The module loader goes too when this was the last site that needed it,
		// and the open-appsec policy loses this host, both in the same change: a
		// policy naming a deleted host would keep inspecting a site that is gone.
		shared, err := m.waf.SharedFiles(ctx, s.Domain)
		if err != nil {
			return DeleteResult{}, err
		}
		files = append(files, shared...)
		policy, err := m.openAppsecPolicyFile(ctx, s.Domain)
		if err != nil {
			return DeleteResult{}, err
		}
		files = append(files, policy)
	}
	services := []confedit.Service{m.svc}
	// A deleted site's host leaves the open-appsec policy, so the agent is told
	// about the new one in the same change, after nginx has reloaded.
	if m.waf != nil {
		was, err := m.waf.Get(ctx, s.Domain)
		if err != nil {
			return DeleteResult{}, err
		}
		if svcs, err := m.wafServices(ctx, s.Domain, was.UsesOpenAppsec()); err != nil {
			return DeleteResult{}, err
		} else {
			services = svcs
		}
	}
	if s.PHPVersion != nil && m.php != nil && s.PHPPool != "" {
		// nginx lets go of the socket first, then FPM drops the pool. The
		// master stays up even when this was its last site (step 2.7).
		files = append(files, confedit.File{Path: m.php.Paths().PoolFile(*s.PHPVersion, s.PHPPool), Delete: true})
		services = append(services, m.php.Service(*s.PHPVersion, false))
	}
	res, err := m.ap.Apply(ctx, confedit.Change{Files: files, Services: services, Reason: "delete site " + s.Domain})
	if err != nil {
		return DeleteResult{}, err
	}
	// Best effort: the directory is empty now, and an empty one is harmless.
	_ = m.fs.Remove(m.p.SiteIncludesDir(s.Domain))

	if err := m.deleteRow(ctx, s.Domain); err != nil {
		return DeleteResult{Apply: res}, fmt.Errorf("sites: the configuration of %s was removed but its database row was not: %w", s.Domain, err)
	}
	out := DeleteResult{Apply: res}
	if s.Root != "" {
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
	rows, err := q.QueryContext(ctx, siteColumns+` ORDER BY s.domain`)
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
		if out[i], err = withRoutes(ctx, q, out[i]); err != nil {
			return nil, err
		}
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
	s, err := scanSite(q.QueryRowContext(ctx, siteColumns+` WHERE s.domain = ?`, d))
	if errors.Is(err, sql.ErrNoRows) {
		return Site{}, fmt.Errorf("%w: %s", ErrNotFound, d)
	}
	if err != nil {
		return Site{}, err
	}
	if s, err = withRoutes(ctx, q, s); err != nil {
		return Site{}, err
	}
	s.Aliases, err = aliasesOf(ctx, q, s.Domain)
	return s, err
}

// siteColumns reads a site row. `proxy_target` is still selected for a rollback
// to a build that predates proxy routes: it is no longer the truth and nothing
// writes it, but a downgraded daemon reads a proxy site the way it used to.
const siteColumns = `SELECT s.domain, s.kind, s.root, s.proxy_target, s.php_version, s.created_at, COALESCE(p.name, ''), s.rewrite
	FROM sites s LEFT JOIN php_pools p ON p.domain = s.domain`

type scanner interface{ Scan(dest ...any) error }

func scanSite(r scanner) (Site, error) {
	var s Site
	var phpVer sql.NullString
	var legacyTarget string
	var created string
	var rewrite string
	if err := r.Scan(&s.Domain, &s.Kind, &s.Root, &legacyTarget, &phpVer, &created, &s.PHPPool, &rewrite); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Site{}, err
		}
		return Site{}, fmt.Errorf("sites: read row: %w", err)
	}
	s.Rewrite = Rewrite(rewrite)
	// Rows written before migration 0020 carry their route in proxy_target and
	// have no site_proxy_routes row, and a row written by this build carries an
	// empty proxy_target. Either way the derived field is the "/" route's target.
	s.ProxyTarget = legacyTarget
	if phpVer.Valid {
		s.PHPVersion = &phpVer.String
	}
	t, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Site{}, fmt.Errorf("sites: %s has a damaged creation time: %w", s.Domain, err)
	}
	s.CreatedAt = t
	return s, nil
}

func proxyRoutesOf(ctx context.Context, q queryer, domain string) ([]ProxyRoute, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT path, target, strip_prefix FROM site_proxy_routes WHERE domain = ? ORDER BY position`, domain)
	if err != nil {
		return nil, fmt.Errorf("sites: proxy routes of %s: %w", domain, err)
	}
	defer func() { _ = rows.Close() }()
	out := []ProxyRoute{}
	for rows.Next() {
		var r ProxyRoute
		var strip int
		if err := rows.Scan(&r.Path, &r.Target, &strip); err != nil {
			return nil, fmt.Errorf("sites: proxy routes of %s: %w", domain, err)
		}
		r.StripPrefix = strip != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// withRoutes fills in a site's proxy routes from the table and derives
// ProxyTarget from the "/" route, so the two spellings agree on every read.
func withRoutes(ctx context.Context, q queryer, s Site) (Site, error) {
	routes, err := proxyRoutesOf(ctx, q, s.Domain)
	if err != nil {
		return Site{}, err
	}
	s.ProxyRoutes = routes
	s.ProxyTarget = ""
	for _, r := range routes {
		if r.Path == "/" {
			s.ProxyTarget = r.Target
		}
	}
	// An unmigrated row (proxy_target set, no route rows) reads as a "/" route.
	if len(routes) == 0 && s.ProxyTarget != "" {
		s.ProxyRoutes = []ProxyRoute{{Path: "/", Target: s.ProxyTarget}}
	}
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
			`INSERT INTO sites (domain, kind, root, proxy_target, php_version, rewrite, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			s.Domain, string(s.Kind), s.Root, "", s.PHPVersion, string(s.Rewrite), s.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("sites: insert %s: %w", s.Domain, err)
		}
		if err := setProxyRoutes(ctx, tx, s.Domain, s.ProxyRoutes); err != nil {
			return err
		}
		if s.PHPVersion != nil {
			if err := insertPool(ctx, tx, s.Domain, s.PHPPool, php.DefaultPoolSettings()); err != nil {
				return err
			}
		}
		return setAliases(ctx, tx, s.Domain, s.Aliases)
	})
}

// setProxyRoutes replaces a site's routes in the order given.
func setProxyRoutes(ctx context.Context, tx *sql.Tx, domain string, routes []ProxyRoute) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_proxy_routes WHERE domain = ?`, domain); err != nil {
		return fmt.Errorf("sites: clear proxy routes of %s: %w", domain, err)
	}
	for i, r := range routes {
		strip := 0
		if r.StripPrefix {
			strip = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO site_proxy_routes (domain, position, path, target, strip_prefix) VALUES (?, ?, ?, ?, ?)`,
			domain, i, r.Path, r.Target, strip); err != nil {
			return fmt.Errorf("sites: add proxy route %s of %s: %w", r.Path, domain, err)
		}
	}
	return nil
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
		if _, err := tx.ExecContext(ctx, `DELETE FROM php_pools WHERE domain = ?`, domain); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM site_proxy_routes WHERE domain = ?`, domain); err != nil {
			return err
		}
		if err := waf.DeleteTx(ctx, tx, domain); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sites WHERE domain = ?`, domain)
		return err
	})
}
