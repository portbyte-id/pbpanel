package php

import (
	"context"
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/native"
	"github.com/portbyte-id/server-panel/internal/osdep"
)

// product is what the shared native helpers call PHP in an error.
const product = "php"

// Build is where a native build's throwaway image and container are named.
const (
	buildTagPrefix       = "panel/php-build"
	buildContainerPrefix = "panel-php-build-"
)

//go:embed builder.Dockerfile
var builderDockerfile string

// buildMarkers is what a finished build's archive must hold: both programs, and
// the release it compiled.
var buildMarkers = []string{"sbin/php-fpm", "bin/php", "version"}

// buildVersionRe accepts what a build may be asked for: a series (8.3), whose
// newest patch the recipe looks up, or one release (8.3.28), which it pins.
// CheckVersion stays the narrower check for the versions a host runs, which are
// series only.
var buildVersionRe = regexp.MustCompile(`^[0-9]\.[0-9]{1,2}(\.[0-9]{1,2})?$`)

// CheckBuildVersion reports whether v is a version the builder can compile.
func CheckBuildVersion(v string) error {
	if !buildVersionRe.MatchString(v) {
		return fmt.Errorf("php: version %q must look like 8.3 or 8.3.28", v)
	}
	return nil
}

// Series is the major.minor part of a version: 8.3.28 is a release of 8.3.
func Series(v string) string {
	major, rest, ok := strings.Cut(v, ".")
	if !ok {
		return v
	}
	minor, _, _ := strings.Cut(rest, ".")
	return major + "." + minor
}

// exactRelease is v when it names one release, and "" when it names a series.
func exactRelease(v string) string {
	if v == Series(v) {
		return ""
	}
	return v
}

// inCore is the extension the core configure line already builds. Asking for one
// of them adds no layer: mbstring is compiled in, and sodium is built shared and
// enabled by the recipe's own conf.d file.
var inCore = map[string]bool{"mbstring": true, "sodium": true}

// DockerfileFor is the build recipe for one build ID on a builder image, for a
// series (the newest patch is looked up) or one release, with its extensions.
//
// The extension list is part of the recipe text, not a build argument: BuildKit
// invalidates every layer after an ARG whose value changes, so an argument would
// recompile the core on every extension change (step 0 measured this).
//
// The core layers do still name the build's prefix, because PHP compiles its
// extension_dir and scan dir in and they must point at the build that is loading
// them. The price is that the core compile is not shared between builds; see
// TestDockerfileForCompilesIntoTheBuildsOwnPrefix.
func DockerfileFor(base, id, version string, exts []Extension, p Paths) []byte {
	prefix := p.BuildPrefix(Series(version), id)
	return []byte(strings.NewReplacer(
		"@BASE@", base,
		"@BUILD_ID@", id,
		"@PHP_SERIES@", Series(version),
		"@PHP_EXACT@", exactRelease(version),
		"@PREFIX@", prefix,
		"@WWW_USER@", WWWUser,
		"@WWW_UID@", strconv.Itoa(WWWUID),
		"@WWW_GID@", strconv.Itoa(WWWGID),
		"@EXTENSIONS@", extensionRecipe(exts, prefix),
		"@MODULE_NAMES@", moduleNames(exts),
	).Replace(builderDockerfile))
}

// extensionRecipe is one layer per extension: its -dev packages, the compile, then
// the conf.d file that enables it. A bundled extension builds from the PHP source
// tree, which is what docker-php-ext-install did; a PECL one comes from pecl.
//
// Every name and flag here is a catalogue constant or a PECL name already checked
// by ParseExtensions, never request input.
func extensionRecipe(exts []Extension, prefix string) string {
	var b strings.Builder
	for _, e := range exts {
		if inCore[e.Name] {
			continue
		}
		if pkgs := sortedUnique(e.Packages); len(pkgs) > 0 {
			fmt.Fprintf(&b, "RUN %s\n", osdep.ImageInstallCommand(pkgs))
		}
		fmt.Fprintf(&b, "RUN set -eux; P=\"%s\"; export PATH=\"$P/bin:$PATH\"; t0=$(date +%%s); \\\n", prefix)
		if e.Kind == "pecl" {
			// `yes ''` answers every configure prompt with its default, so an
			// extension that asks several (memcached) still builds unattended.
			fmt.Fprintf(&b, "    yes '' | pecl install %s; \\\n", e.Name)
		} else {
			fmt.Fprintf(&b, "    ( cd ext/%s && phpize && ./configure%s && make -j\"$(nproc)\" && make install ); \\\n",
				e.Name, flagSuffix(configureFlags(e)))
		}
		fmt.Fprintf(&b, "    echo \"TIMING %s $(( $(date +%%s) - t0 ))s\"; \\\n", e.Name)
		fmt.Fprintf(&b, "    echo \"%s=%s\" > \"$P/etc/conf.d/%s\"\n", iniDirective(e), e.Name, iniFile(e))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// configureFlags is what ./configure takes for an extension. The catalogue's
// Configure list is it, except for opcache, whose own flag the catalogue never
// needed: docker-php-ext-install passed it itself.
func configureFlags(e Extension) string {
	if e.Name == "opcache" {
		return "--enable-opcache"
	}
	return strings.Join(e.Configure, " ")
}

// flagSuffix puts a space before the flags, and nothing when there are none.
func flagSuffix(flags string) string {
	if flags == "" {
		return ""
	}
	return " " + flags
}

// iniDirective is what loads an extension: opcache and xdebug are Zend
// extensions, not PHP ones, and `extension=` would not load them.
func iniDirective(e Extension) string {
	if e.Name == "opcache" || e.Name == "xdebug" {
		return "zend_extension"
	}
	return "extension"
}

// iniFile is an extension's conf.d file. The numeric prefix is the load order:
// opcache first, the rest after the core's own files.
func iniFile(e Extension) string {
	if e.Name == "opcache" {
		return "10-opcache.ini"
	}
	return "30-" + e.Name + ".ini"
}

// moduleNames is the `php -m` names the build must list, quoted so a module name
// with a space ("Zend OPcache") is one word to the shell.
func moduleNames(exts []Extension) string {
	var out []string
	for _, e := range exts {
		out = append(out, shellQuote(e.Module))
	}
	return strings.Join(out, " ")
}

// shellQuote wraps s in single quotes.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Builder compiles PHP natively: on a builder image that matches the host, so the
// binaries link the host's own glibc, leaving a finished prefix the panel reads
// out of the image and keeps. The builder image and container are thrown away.
type Builder struct {
	Eng native.BuildEngine
	P   Paths
	// OSRelease is the file the builder image is matched on; empty means
	// /etc/os-release.
	OSRelease string
}

// NewBuilder returns the builder of a Paths layout.
func NewBuilder(eng native.BuildEngine, p Paths) *Builder {
	return &Builder{Eng: eng, P: p}
}

// Build compiles build id of version with exts and leaves it at
// P.BuildPrefix(version, id). A host the builder cannot match is refused before
// Docker is touched.
func (b *Builder) Build(ctx context.Context, id, version string, exts []Extension, progress docker.Progress) error {
	if err := CheckBuildVersion(version); err != nil {
		return err
	}
	series := Series(version)
	return native.DockerBuilder{
		Eng:       b.Eng,
		OSRelease: b.OSRelease,
		Spec: native.Spec{
			Product:         product,
			TagPrefix:       buildTagPrefix,
			ContainerPrefix: buildContainerPrefix,
			BuildDir:        func(id string) string { return b.P.BuildPrefix(series, id) },
			VersionsDir:     func() string { return b.P.VersionsDir(series) },
			Dockerfile: func(base, id, v string) []byte {
				return DockerfileFor(base, id, v, exts, b.P)
			},
			Required: buildMarkers,
		},
	}.Build(ctx, id, version, progress)
}
