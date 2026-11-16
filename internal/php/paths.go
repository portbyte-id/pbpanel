package php

import (
	"fmt"
	"path"
	"regexp"
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

// Image is the derived image: the official one plus the `www` account, the same
// approach as nginx (decision 1.1).
func Image(ver string) string { return "panel/php:" + ver + "-fpm" }

// ServiceName is the confedit service name, which keys the apply lock.
func ServiceName(ver string) string { return "php-" + ver }

// Paths is where PHP's files live. Like nginx.Paths every path is a Linux path
// built with package path: the same string is written into config files and
// mounted into containers.
type Paths struct {
	// Root is the install root, `/www` in production.
	Root string
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
