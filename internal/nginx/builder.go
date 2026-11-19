package nginx

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/portbyte-id/server-panel/internal/docker"
)

// Names. A build is stored under VersionsDir by ID; which one serves is the
// pointer kept by package images and the CurrentLink symlink that mirrors it.
const (
	// Version is the series that serves until the operator picks another.
	Version = "1.30"
	// Repo names the family of builds in the catalogue. Builds are directories
	// now, not images, but the catalogue keys its rows by this reference.
	Repo = "panel/nginx"
	// ServiceName is the name the config editor knows nginx by.
	ServiceName = "nginx"
	// UnitName is the systemd unit that runs nginx.
	UnitName = "panel-nginx.service"
)

// BuildSource describes what every build is made of, for the Nginx page.
const BuildSource = "nginx.org source, OpenSSL " + openSSLSeries + ", ModSecurity"

// SuggestedVersions are the nginx series the Nginx page offers, newest first. Any
// MAJOR.MINOR nginx.org publishes works, and so does a full MAJOR.MINOR.PATCH that
// pins one release; one that does not exist fails at the download inside the build,
// before anything serving is touched.
var SuggestedVersions = []string{"1.30", "1.29", "1.28"}

var versionPattern = regexp.MustCompile(`^[1-9][0-9]?\.[0-9]{1,2}(\.[0-9]{1,2})?$`)

// seriesPattern is the MAJOR.MINOR part of a version, which the build matches
// tarball names on.
var seriesPattern = regexp.MustCompile(`^[1-9][0-9]?\.[0-9]{1,2}$`)

// CheckVersion reports whether v is a version the panel will build: a series such
// as 1.30, whose newest patch the build looks up, or a release such as 1.30.1.
func CheckVersion(v string) error {
	if !versionPattern.MatchString(v) {
		return fmt.Errorf("nginx: version %q must look like 1.30 or 1.30.1", v)
	}
	return nil
}

// Series is the MAJOR.MINOR part of a version: 1.30.1 is a release of 1.30.
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
	if seriesPattern.MatchString(v) {
		return ""
	}
	return v
}

// NormalizeVersion maps a version stored before there was one build to its
// series: the post-quantum flavor was "pqc-1.30", and every build is that flavor now.
func NormalizeVersion(v string) string { return strings.TrimPrefix(v, "pqc-") }

// openSSLSeries is the OpenSSL series nginx links statically. 3.5 is the first
// with ML-KEM and ML-DSA in the default provider, and it is a long-term release.
// With it nginx offers the hybrid X25519MLKEM768 group out of the box, so no
// ssl_ecdh_curve line is needed.
const openSSLSeries = "3.5"

// Versions of libmodsecurity (ModSecurity v3) and of its nginx connector, built
// as a dynamic module. It is only loaded by a `load_module` line, so nginx behaves
// like a plain build until the configuration asks for it.
const (
	modSecurityVersion      = "3.0.14"
	modSecurityNginxVersion = "1.0.4"
)

// The OWASP Core Rule Set shipped with every build, the rules ModSecurity runs when
// a site's firewall is on. The release tarball is pinned and checked against the
// digest GitHub publishes for the asset, so a build never takes rules nobody
// looked at.
const (
	// CRSVersion is the Core Rule Set release.
	CRSVersion = "4.30.0"
	crsSHA256  = "3d678a41fd5aade34760127fef5dd64fd7a77848913fc0f70dde0cf467c94427"
)

//go:embed builder.Dockerfile
var builderDockerfile string

// ErrUnsupportedHost is a host the builder cannot match yet.
var ErrUnsupportedHost = errors.New("nginx: no builder image matches this host")

// Host is the part of /etc/os-release a build is matched on.
type Host struct {
	ID        string // ubuntu, debian, rocky
	VersionID string // 26.04, 12, 9.8
}

// ReadHost parses an os-release file.
func ReadHost(file string) (Host, error) {
	f, err := os.Open(file)
	if err != nil {
		return Host{}, fmt.Errorf("nginx: read the host release: %w", err)
	}
	defer func() { _ = f.Close() }()
	return parseHost(f)
}

func parseHost(r io.Reader) (Host, error) {
	var h Host
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			h.ID = v
		case "VERSION_ID":
			h.VersionID = v
		}
	}
	if err := sc.Err(); err != nil {
		return Host{}, err
	}
	if h.ID == "" || h.VersionID == "" {
		return Host{}, errors.New("nginx: the host release names no ID or VERSION_ID")
	}
	return h, nil
}

// BuilderImage is the image a build compiles on: the same distro and release as
// the host, so the binary links the host's own glibc. The EL family is not built
// yet (development plan M9: it needs a dnf builder and SELinux labels).
func BuilderImage(h Host) (string, error) {
	switch h.ID {
	case "ubuntu", "debian":
		return h.ID + ":" + h.VersionID, nil
	}
	return "", fmt.Errorf("%w: %s %s is not supported yet", ErrUnsupportedHost, h.ID, h.VersionID)
}

// DockerfileFor is the build recipe for one build ID on a builder image, for a
// series (the newest patch is looked up) or one release.
func DockerfileFor(base, id, version string, p Paths) []byte {
	return []byte(strings.NewReplacer(
		"@BASE@", base,
		"@BUILD_ID@", id,
		"@NGINX_SERIES_RE@", strings.ReplaceAll(Series(version), ".", `\.`),
		"@NGINX_EXACT@", exactRelease(version),
		"@OPENSSL_SERIES@", openSSLSeries,
		"@MODSEC_VERSION@", modSecurityVersion,
		"@MODSEC_NGINX_VERSION@", modSecurityNginxVersion,
		"@CRS_VERSION@", CRSVersion,
		"@CRS_SHA256@", crsSHA256,
		"@PREFIX@", p.BuildDir(id),
		"@CONF_PATH@", p.ConfPath(),
		"@LOG_DIR@", p.MainLogDir(),
		"@RUN_DIR@", p.RunDir(),
		"@CACHE_DIR@", p.CacheDir(),
		"@WWW_USER@", WWWUser,
	).Replace(builderDockerfile))
}

func say(p docker.Progress, line string) {
	if p != nil {
		p(line)
	}
}

// Builder makes the build with one ID: a finished directory under VersionsDir.
type Builder interface {
	Build(ctx context.Context, id, version string, progress docker.Progress) error
}

// BuildEngine is the part of the Docker client the builder uses. Docker only
// compiles: nothing it makes keeps running.
type BuildEngine interface {
	ImageBuild(ctx context.Context, opts docker.BuildOptions, progress docker.Progress) (docker.Image, error)
	ContainerCreate(ctx context.Context, name string, cfg docker.ContainerConfig) (string, error)
	ContainerArchive(ctx context.Context, id, path string) (io.ReadCloser, error)
	ContainerRemove(ctx context.Context, id string, force bool) error
	ImageRemove(ctx context.Context, ref string) error
}

// DockerBuilder compiles a build in a builder image and reads the prefix out of it.
type DockerBuilder struct {
	Eng BuildEngine
	P   Paths
	// OSRelease is the file the builder image is matched on; empty means
	// /etc/os-release.
	OSRelease string
}

// Build compiles build id of version and leaves it at P.BuildDir(id). The builder
// image and container are removed whether or not it worked.
func (b DockerBuilder) Build(ctx context.Context, id, version string, progress docker.Progress) error {
	file := b.OSRelease
	if file == "" {
		file = "/etc/os-release"
	}
	host, err := ReadHost(file)
	if err != nil {
		return err
	}
	base, err := BuilderImage(host)
	if err != nil {
		return err
	}
	say(progress, "building nginx "+version+" on "+base)

	tag := "panel/nginx-build:" + id
	bg := context.WithoutCancel(ctx)
	if _, err := b.Eng.ImageBuild(ctx, docker.BuildOptions{
		Tag: tag, Dockerfile: DockerfileFor(base, id, version, b.P), Pull: true,
		Labels: map[string]string{docker.LabelManaged: "true"},
	}, progress); err != nil {
		return err
	}
	defer func() { _ = b.Eng.ImageRemove(bg, tag) }()

	name := "panel-nginx-build-" + id
	cid, err := b.Eng.ContainerCreate(ctx, name, docker.ContainerConfig{
		Image: tag, Cmd: []string{"true"},
		Labels: map[string]string{docker.LabelManaged: "true"},
	})
	if err != nil {
		return err
	}
	defer func() { _ = b.Eng.ContainerRemove(bg, cid, true) }()

	say(progress, "copying the build out of the builder image")
	rc, err := b.Eng.ContainerArchive(ctx, cid, b.P.BuildDir(id))
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	return ExtractBuild(rc, b.P.VersionsDir(), id)
}
