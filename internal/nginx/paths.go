package nginx

import (
	"path"

	"github.com/portbyte-id/server-panel/internal/webroots"
)

// Paths is where nginx's files live. Every path is a Linux path inside the
// install root and is built with package path, never filepath: the same string
// is written into config files and mounted into the container, so it must not
// change with the OS the daemon was compiled on.
type Paths struct {
	// Root is the install root, `/www` in production.
	Root string
	// Roots is the allowlist of folders outside the sites tree that sites may
	// use as web roots. It is read on every spec and render, so a change on the
	// Settings page applies without rebuilding the service. Nil allows none.
	Roots *webroots.Holder
}

// ServesRoot reports whether a site's web root is one nginx can see: inside the
// sites tree or inside an allowed folder.
func (p Paths) ServesRoot(root string) bool {
	return webroots.Servable(root, p.SitesRoot(), p.Roots)
}

// Dir is the generated nginx config tree.
func (p Paths) Dir() string { return path.Join(p.Root, "panel", "nginx") }

// ConfPath is nginx.conf, generated once and then the operator's.
func (p Paths) ConfPath() string { return path.Join(p.Dir(), "nginx.conf") }

// MainIncludePath holds the main-context directives the settings form owns.
func (p Paths) MainIncludePath() string { return path.Join(p.Dir(), "panel-main.conf") }

// HTTPIncludePath holds the http-context directives the settings form owns.
func (p Paths) HTTPIncludePath() string { return path.Join(p.Dir(), "panel-http.conf") }

// SitesDir holds one file per site.
func (p Paths) SitesDir() string { return path.Join(p.Dir(), "sites") }

// SiteFile is a site's own file, which the operator may edit freely.
func (p Paths) SiteFile(domain string) string { return path.Join(p.SitesDir(), domain+".conf") }

// IncludesDir holds one directory of panel-owned include files per site.
func (p Paths) IncludesDir() string { return path.Join(p.Dir(), "includes") }

// SiteIncludesDir is the directory of one site's include files.
func (p Paths) SiteIncludesDir(domain string) string { return path.Join(p.IncludesDir(), domain) }

// SiteInclude is one of a site's include files.
func (p Paths) SiteInclude(domain, name string) string {
	return path.Join(p.SiteIncludesDir(domain), name)
}

// SiteLogDir holds every site's access and error log.
func (p Paths) SiteLogDir() string { return path.Join(p.Root, "panel", "logs", "sites") }

// AccessLog and ErrorLog are a site's log files.
func (p Paths) AccessLog(domain string) string {
	return path.Join(p.SiteLogDir(), domain+".access.log")
}
func (p Paths) ErrorLog(domain string) string { return path.Join(p.SiteLogDir(), domain+".error.log") }

// RunDir holds the PHP-FPM sockets, shared with the PHP containers.
func (p Paths) RunDir() string { return path.Join(p.Root, "panel", "run") }

// SSLDir holds the panel's own certificate, the ACME account key and the site
// certificates. It is never mounted into a container as a whole.
func (p Paths) SSLDir() string { return path.Join(p.Root, "panel", "ssl") }

// SiteCertsDir holds one directory of certificate files per site. It is the
// only part of SSLDir that holds site keys, and the nginx container mounts it
// read-only.
func (p Paths) SiteCertsDir() string { return path.Join(p.SSLDir(), "sites") }

// SiteCertDir is the directory of one site's certificate files.
func (p Paths) SiteCertDir(domain string) string { return path.Join(p.SiteCertsDir(), domain) }

// ACMEWebroot is where HTTP-01 challenge tokens are written and nginx serves
// them from.
func (p Paths) ACMEWebroot() string { return path.Join(p.SSLDir(), "acme-webroot") }

// SitesRoot is the directory whose subdirectories are web roots.
func (p Paths) SitesRoot() string { return path.Join(p.Root, "services", "sites") }

// SiteRoot is a site's default web root.
func (p Paths) SiteRoot(domain string) string { return path.Join(p.SitesRoot(), domain) }

// The shared account nginx workers run as and web roots belong to. These repeat
// install.WWWUser, install.WWWUID and install.WWWGID: the install package
// imports the HTTP layer, which imports this one, so it cannot be imported
// here. A test in cmd/panel holds the two sets equal.
const (
	WWWUser = "www"
	WWWUID  = 1500
	WWWGID  = 1500
)
