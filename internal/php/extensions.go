package php

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// MaxExtensions bounds a build request. Each extension is a layer's worth of
// compile time; a longer list is a mistake or an attack on the build host.
const MaxExtensions = 32

// Extension is one entry of the curated catalogue: what a build needs to add a
// PHP extension to the official image.
type Extension struct {
	// Name is what the request and the UI use.
	Name string `json:"name"`
	// Module is the name `php -m` lists the extension under.
	Module      string `json:"module"`
	Description string `json:"description"`
	// Kind is `bundled` (compiled from the PHP source with docker-php-ext-install)
	// or `pecl` (fetched and compiled with pecl).
	Kind string `json:"kind"`
	// Packages are the apt packages the compile needs, the -dev ones.
	Packages []string `json:"packages"`
	// Configure is the docker-php-ext-configure argument list, if any. Its
	// entries are constants of this file, never request input.
	Configure []string `json:"-"`
}

// catalogue is the curated list. The bundled entries compile on the official
// `php:<ver>-fpm` images with the packages named here; a pecl entry needs the
// build tools of the image's $PHPIZE_DEPS, which a build installs whenever it
// has a pecl entry. The list is the same for every supported version; a
// version an entry does not compile on fails its build, with the output kept.
var catalogue = []Extension{
	{Name: "apcu", Module: "apcu", Description: "User data cache", Kind: "pecl"},
	{Name: "bcmath", Module: "bcmath", Description: "Arbitrary-precision arithmetic", Kind: "bundled"},
	{Name: "bz2", Module: "bz2", Description: "bzip2 compression streams", Kind: "bundled", Packages: []string{"libbz2-dev"}},
	{Name: "calendar", Module: "calendar", Description: "Calendar conversions", Kind: "bundled"},
	{Name: "exif", Module: "exif", Description: "Image metadata", Kind: "bundled"},
	{Name: "gd", Module: "gd", Description: "Image creation and manipulation", Kind: "bundled",
		Packages:  []string{"libfreetype-dev", "libjpeg-dev", "libpng-dev", "libwebp-dev"},
		Configure: []string{"--with-freetype", "--with-jpeg", "--with-webp"}},
	{Name: "igbinary", Module: "igbinary", Description: "Compact binary serialiser", Kind: "pecl"},
	{Name: "imagick", Module: "imagick", Description: "ImageMagick bindings", Kind: "pecl", Packages: []string{"libmagickwand-dev"}},
	{Name: "intl", Module: "intl", Description: "Internationalisation (ICU)", Kind: "bundled", Packages: []string{"libicu-dev"}},
	{Name: "ldap", Module: "ldap", Description: "LDAP directory client", Kind: "bundled", Packages: []string{"libldap2-dev"},
		Configure: []string{`--with-libdir="lib/$(uname -m)-linux-gnu"`}},
	{Name: "mbstring", Module: "mbstring", Description: "Multibyte strings", Kind: "bundled"},
	{Name: "memcached", Module: "memcached", Description: "Memcached client", Kind: "pecl", Packages: []string{"libmemcached-dev", "zlib1g-dev"}},
	{Name: "mongodb", Module: "mongodb", Description: "MongoDB driver", Kind: "pecl", Packages: []string{"libssl-dev"}},
	{Name: "mysqli", Module: "mysqli", Description: "MySQL improved client", Kind: "bundled"},
	{Name: "opcache", Module: "Zend OPcache", Description: "Bytecode cache", Kind: "bundled"},
	{Name: "pcntl", Module: "pcntl", Description: "Process control", Kind: "bundled"},
	{Name: "pdo_mysql", Module: "pdo_mysql", Description: "PDO driver for MySQL", Kind: "bundled"},
	{Name: "pdo_pgsql", Module: "pdo_pgsql", Description: "PDO driver for PostgreSQL", Kind: "bundled", Packages: []string{"libpq-dev"}},
	{Name: "pgsql", Module: "pgsql", Description: "PostgreSQL client", Kind: "bundled", Packages: []string{"libpq-dev"}},
	{Name: "redis", Module: "redis", Description: "Redis client", Kind: "pecl"},
	{Name: "sockets", Module: "sockets", Description: "Low-level socket API", Kind: "bundled"},
	{Name: "sodium", Module: "sodium", Description: "libsodium cryptography", Kind: "bundled", Packages: []string{"libsodium-dev"}},
	{Name: "soap", Module: "soap", Description: "SOAP client and server", Kind: "bundled", Packages: []string{"libxml2-dev"}},
	{Name: "xdebug", Module: "xdebug", Description: "Debugger and profiler (development only)", Kind: "pecl"},
	{Name: "xsl", Module: "xsl", Description: "XSLT transformations", Kind: "bundled", Packages: []string{"libxslt1-dev"}},
	{Name: "zip", Module: "zip", Description: "Zip archives", Kind: "bundled", Packages: []string{"libzip-dev"}},
}

// pecl names are written into a RUN line, so only this shape is accepted: a
// lowercase package name with no version, shell syntax, or path.
var peclNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}$`)

// Catalogue returns the curated extensions, sorted by name.
func Catalogue() []Extension {
	out := slices.Clone(catalogue)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ParseExtensions turns a request's extension names into the build's list. A
// catalogue name is used as it is; any other name must be a PECL package name,
// which the Dockerfile installs with pecl. Duplicates are dropped and the result
// is sorted, so the same request always makes the same image.
func ParseExtensions(names []string) ([]Extension, error) {
	seen := map[string]bool{}
	var out []Extension
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if seen[name] {
			continue
		}
		seen[name] = true
		if ext, ok := lookup(name); ok {
			out = append(out, ext)
			continue
		}
		if !peclNameRe.MatchString(name) {
			return nil, bad("extensions", "%q is not in the catalogue and is not a PECL package name", raw)
		}
		out = append(out, Extension{Name: name, Module: name, Description: "PECL package " + name, Kind: "pecl"})
	}
	if len(out) > MaxExtensions {
		return nil, bad("extensions", "at most %d extensions in one build", MaxExtensions)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func lookup(name string) (Extension, bool) {
	for _, e := range catalogue {
		if e.Name == name {
			return e, true
		}
	}
	return Extension{}, false
}

// ExtensionNames returns the names of a list, for storage and for the UI.
func ExtensionNames(exts []Extension) []string {
	out := make([]string, 0, len(exts))
	for _, e := range exts {
		out = append(out, e.Name)
	}
	return out
}

// Dockerfile is the derived image of a version: the official one, the `www`
// account at the fixed IDs (decision 1.1), and the extensions. Every line that
// reaches the shell is a constant from the catalogue or a validated PECL name.
func Dockerfile(ver string, exts []Extension) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", BaseImage(ver))
	fmt.Fprintf(&b, "RUN groupadd -g %d %s && useradd -u %d -g %s -M -d /nonexistent -s /usr/sbin/nologin %s\n",
		WWWGID, WWWUser, WWWUID, WWWUser, WWWUser)

	var packages []string
	var bundled, pecl []Extension
	for _, e := range exts {
		packages = append(packages, e.Packages...)
		if e.Kind == "pecl" {
			pecl = append(pecl, e)
		} else {
			bundled = append(bundled, e)
		}
	}
	packages = sortedUnique(packages)
	if len(packages) > 0 || len(pecl) > 0 {
		install := "apt-get update && apt-get install -y --no-install-recommends"
		if len(packages) > 0 {
			install += " " + strings.Join(packages, " ")
		}
		if len(pecl) > 0 {
			install += " ${PHPIZE_DEPS}"
		}
		fmt.Fprintf(&b, "RUN %s && rm -rf /var/lib/apt/lists/*\n", install)
	}
	for _, e := range bundled {
		if len(e.Configure) > 0 {
			fmt.Fprintf(&b, "RUN docker-php-ext-configure %s %s\n", e.Name, strings.Join(e.Configure, " "))
		}
	}
	if len(bundled) > 0 {
		fmt.Fprintf(&b, "RUN docker-php-ext-install -j$(nproc) %s\n", strings.Join(ExtensionNames(bundled), " "))
	}
	if len(pecl) > 0 {
		names := strings.Join(ExtensionNames(pecl), " ")
		fmt.Fprintf(&b, "RUN pecl install %s && docker-php-ext-enable %s\n", names, names)
	}
	return []byte(b.String())
}

func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	sort.Strings(out)
	return slices.Compact(out)
}

// modulesOf reads `php -m`: one module per line under the `[PHP Modules]` and
// `[Zend Modules]` headings. Bracketed lines are headings, not modules.
func modulesOf(out string) map[string]bool {
	mods := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		mods[strings.ToLower(line)] = true
	}
	return mods
}

// missingModules lists the extensions whose module `php -m` output does not show.
func missingModules(out string, exts []Extension) []string {
	mods := modulesOf(out)
	var missing []string
	for _, e := range exts {
		if !mods[strings.ToLower(e.Module)] {
			missing = append(missing, e.Name)
		}
	}
	return missing
}
