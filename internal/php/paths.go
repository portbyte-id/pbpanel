package php

import (
	"fmt"
	"path"
	"regexp"

	"github.com/portbyte-id/server-panel/internal/webroots"
)

// The shared account pools run as and sockets belong to. These repeat
// install.WWWUser/WWWUID/WWWGID and nginx's copies for the same reason nginx
// repeats them: install imports the HTTP layer. A test in cmd/panel holds the
// sets equal.
const (
	WWWUser = "www"
	WWWUID  = 1500
	WWWGID  = 1500
)

var versionRe = regexp.MustCompile(`^[0-9]\.[0-9]{1,2}$`)

// CheckVersion accepts a `major.minor` version and nothing else. The version is
// written into paths, container names and image references, so this check is
// what keeps a request parameter from becoming any of those.
func CheckVersion(v string) error {
	if !versionRe.MatchString(v) {
		return fmt.Errorf("php: %q is not a PHP version such as 8.3", v)
	}
	return nil
}

// ContainerName is the container of one version.
func ContainerName(ver string) string { return "panel-php-" + ver }

// BaseImage is the official image a version runs on.
func BaseImage(ver string) string { return "php:" + ver + "-fpm" }

// Repo is the repository of the derived images: `panel/php:<ver>-<build-id>`.
// Which build serves is a pointer in the database (package images), never a
// mutable tag.
const Repo = "panel/php"

// LegacyImage is the reference the derived image had before builds had IDs. It
// is adopted at boot and not written again.
func LegacyImage(ver string) string { return Repo + ":" + ver + "-fpm" }

// BuildDir is the build context of one build on the host. The panel writes the
// Dockerfile there and sends the directory to the Engine.
//
// The native runtime does not use it: it compiles in a builder image and extracts
// the finished prefix to VersionsDir. It stays until the Docker path is removed.
func (p Paths) BuildDir(ver, id string) string {
	return path.Join(p.Root, "panel", "build", "php", ver, id)
}

// ServiceName is the confedit service name, which keys the apply lock.
func ServiceName(ver string) string { return "php-" + ver }

// Paths is where PHP's files live. Like nginx.Paths every path is a Linux path
// built with package path: the same string is written into config files and
// mounted into containers.
type Paths struct {
	// Root is the install root, `/www` in production.
	Root string
	// Roots is the allowlist of folders outside the sites tree that sites may
	// use as web roots; see nginx.Paths.Roots. Nil allows none.
	Roots *webroots.Holder
}

// Base holds one directory per version.
func (p Paths) Base() string { return path.Join(p.Root, "panel", "php") }

// Dir is one version's config tree.
func (p Paths) Dir(ver string) string { return path.Join(p.Base(), ver) }

// FPMConf is the FPM main config, owned by the panel.
func (p Paths) FPMConf(ver string) string { return path.Join(p.Dir(ver), "php-fpm.conf") }

// IniPath is php.ini: copied from the image once, then the operator's.
func (p Paths) IniPath(ver string) string { return path.Join(p.Dir(ver), "php.ini") }

// ConfDir is scanned after the image's own conf.d.
func (p Paths) ConfDir(ver string) string { return path.Join(p.Dir(ver), "conf.d") }

// FormIni holds the directives the settings form owns. The name sorts last, so
// the form wins over php.ini and over the image's extension files.
func (p Paths) FormIni(ver string) string { return path.Join(p.ConfDir(ver), "zz-panel.ini") }

// PoolDir holds one pool file per PHP site.
func (p Paths) PoolDir(ver string) string { return path.Join(p.Dir(ver), "pool.d") }

// PoolFile is a site's pool file.
func (p Paths) PoolFile(ver, pool string) string { return path.Join(p.PoolDir(ver), pool+".conf") }

// RunDir holds the sockets, shared with the nginx container.
func (p Paths) RunDir() string { return path.Join(p.Root, "panel", "run") }

// Socket is a pool's socket.
func (p Paths) Socket(ver, pool string) string {
	return path.Join(p.RunDir(), "php-"+ver+"-"+pool+".sock")
}

// SiteLogDir is the directory of per-site logs, the one nginx writes to as well.
func (p Paths) SiteLogDir() string { return path.Join(p.Root, "panel", "logs", "sites") }

// ErrorLog is a site's PHP error log.
func (p Paths) ErrorLog(domain string) string {
	return path.Join(p.SiteLogDir(), domain+".php.log")
}

// SitesRoot is the directory whose subdirectories are web roots.
func (p Paths) SitesRoot() string { return path.Join(p.Root, "services", "sites") }

// VersionsDir holds one directory per native PHP build: php-fpm, the CLI, the
// shared extensions and the libraries they need. Builds are never written in
// place, as nginx's are not.
func (p Paths) VersionsDir(ver string) string { return path.Join(p.Dir(ver), "versions") }

// BuildPrefix is where the build with this ID lives. It is also the build's
// compiled-in prefix, so extension_dir and the compiled-in scan dir resolve
// against the build that is loading them, candidate or serving.
//
// It is not BuildDir: BuildDir(ver, id) is the Docker build context, a different
// thing the native runtime never writes.
func (p Paths) BuildPrefix(ver, id string) string {
	return path.Join(p.VersionsDir(ver), id)
}

// CurrentLink is the symlink to the build that serves. A swap is one rename of
// this link, so the unit and the configuration name a path that never changes.
func (p Paths) CurrentLink(ver string) string { return path.Join(p.Dir(ver), "current") }

// Binary is the FPM master of the build that serves.
func (p Paths) Binary(ver string) string {
	return path.Join(p.CurrentLink(ver), "sbin", "php-fpm")
}

// CLIBinary is the php command-line binary of the build that serves.
func (p Paths) CLIBinary(ver string) string { return path.Join(p.CurrentLink(ver), "bin", "php") }

// BinaryOf is the FPM master of one build, for checking a candidate before it
// serves.
func (p Paths) BinaryOf(ver, id string) string {
	return path.Join(p.BuildPrefix(ver, id), "sbin", "php-fpm")
}

// CLIBinaryOf is the CLI of one build.
func (p Paths) CLIBinaryOf(ver, id string) string {
	return path.Join(p.BuildPrefix(ver, id), "bin", "php")
}

// VersionFile is where a build records the PHP release it compiled. A series
// build picks its patch release while it runs, so the series does not say which
// one is on disk.
func (p Paths) VersionFile(ver, id string) string {
	return path.Join(p.BuildPrefix(ver, id), "version")
}

// ProductionIniFile is the build's php.ini-production, what a version's php.ini is
// first copied from.
func (p Paths) ProductionIniFile(ver, id string) string {
	return path.Join(p.BuildPrefix(ver, id), "etc", "php.ini-production")
}

// FPMErrorLog is the FPM master's own log, not a site's. Under systemd FPM cannot
// open /proc/self/fd/2 or /dev/stderr, so it logs to a file; the unit names it in
// ReadWritePaths and the rendered php-fpm.conf in [global] error_log.
func (p Paths) FPMErrorLog(ver string) string {
	return path.Join(p.Root, "panel", "logs", "php-"+ver+"-fpm.log")
}

// ServesRoot reports whether a pool's web root is one the PHP container can see:
// inside the sites tree or inside an allowed folder; see nginx.Paths.ServesRoot.
func (p Paths) ServesRoot(root string) bool {
	return webroots.Servable(root, p.SitesRoot(), p.Roots)
}
