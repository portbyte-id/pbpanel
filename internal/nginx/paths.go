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

// VersionsDir holds one directory per nginx build: its binary, module and the
// libraries they need. Builds are never written in place.
func (p Paths) VersionsDir() string { return path.Join(p.Dir(), "versions") }

// BuildDir is where the build with this ID lives. It is also the build's compiled-in
// prefix, so the module path in a config resolves against the build that loads it.
func (p Paths) BuildDir(id string) string { return path.Join(p.VersionsDir(), id) }

// CurrentLink is the symlink to the build that serves. A swap is one rename of
// this link, so the unit and the configuration name a path that never changes.
func (p Paths) CurrentLink() string { return path.Join(p.Dir(), "current") }

// Binary is the nginx executable of the build that serves.
func (p Paths) Binary() string { return path.Join(p.CurrentLink(), "sbin", "nginx") }

// BinaryOf is the executable of one build, for checking a candidate before it serves.
func (p Paths) BinaryOf(id string) string { return path.Join(p.BuildDir(id), "sbin", "nginx") }

// MimeTypes is the media type table shipped with every build.
func (p Paths) MimeTypes() string { return path.Join(p.CurrentLink(), "conf", "mime.types") }

// FastCGIParams is the FastCGI variable table shipped with every build.
func (p Paths) FastCGIParams() string { return path.Join(p.CurrentLink(), "conf", "fastcgi_params") }

// ModSecDir holds ModSecurity's base files and the Core Rule Set, shipped with
// every build.
func (p Paths) ModSecDir() string { return path.Join(p.CurrentLink(), "modsec") }

// CRSDir is the OWASP Core Rule Set of the build that serves.
func (p Paths) CRSDir() string { return path.Join(p.ModSecDir(), "crs") }

// CacheDir holds nginx's temporary files.
func (p Paths) CacheDir() string { return path.Join(p.Root, "panel", "cache", "nginx") }

// MainLogDir holds nginx's own logs, not a site's.
func (p Paths) MainLogDir() string { return path.Join(p.Root, "panel", "logs") }

// MainErrorLog is the error log nginx opens before it has read its configuration.
func (p Paths) MainErrorLog() string { return path.Join(p.MainLogDir(), "nginx-error.log") }

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

// WAFDir holds what the firewall renders: http-context files, the module loader
// and one ModSecurity rules file per site. It sits inside Dir, so the container
// reads it through the same read-only mount as the rest of the configuration.
func (p Paths) WAFDir() string { return path.Join(p.Dir(), "waf") }

// WAFMainDir holds main-context files (the module loader). panel-main.conf
// includes every file in it.
func (p Paths) WAFMainDir() string { return path.Join(p.WAFDir(), "main") }

// WAFHTTPDir holds one http-context file per site: rate-limit zones and the
// address and path maps. panel-http.conf includes every file in it.
func (p Paths) WAFHTTPDir() string { return path.Join(p.WAFDir(), "http") }

// WAFRulesDir holds one ModSecurity rules file per site.
func (p Paths) WAFRulesDir() string { return path.Join(p.WAFDir(), "rules") }

// WAFHTTPFile is a site's http-context firewall file.
func (p Paths) WAFHTTPFile(domain string) string {
	return path.Join(p.WAFHTTPDir(), domain+".conf")
}

// WAFRulesFile is a site's ModSecurity rules file.
func (p Paths) WAFRulesFile(domain string) string {
	return path.Join(p.WAFRulesDir(), domain+".conf")
}

// WAFAuditLog is where ModSecurity writes a site's audit events, in the writable
// site log directory.
func (p Paths) WAFAuditLog(domain string) string {
	return path.Join(p.SiteLogDir(), domain+".waf-audit.log")
}

// WAFModuleFile is the main-context file that loads the ModSecurity module.
func (p Paths) WAFModuleFile() string { return path.Join(p.WAFMainDir(), "modsecurity.conf") }

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
