package native

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/jobs"
)

// IndexVersion is the layout of index.json this code reads. A newer layout is
// refused rather than half understood.
const IndexVersion = 1

// Limits on what a prebuilt source may send. The index is a few kilobytes and a
// package a few tens of megabytes; these only stop a runaway stream.
const (
	maxIndexBytes   = 4 << 20
	maxPackageBytes = 1 << 30
)

// ErrNoPackage means the source holds no package for this host and request. It is
// not a failure of the source: the caller builds from source instead.
var ErrNoPackage = errors.New("no prebuilt package for this host")

// Package is one prebuilt build a CI job published. Everything a host needs to
// decide that it can use it is here, so nothing is guessed from a file name.
type Package struct {
	Product string `json:"product"`
	// Release is the version compiled, such as 1.30.2.
	Release string `json:"release"`
	// ID is the build ID compiled into the prefix; the host keeps the build under it.
	ID        string `json:"id"`
	Distro    string `json:"distro"`
	VersionID string `json:"version_id"`
	Arch      string `json:"arch"`
	// Root is the install root compiled into the paths the build uses.
	Root string `json:"root"`
	// Recipe identifies the recipe text and pinned inputs the build was made from.
	Recipe string `json:"recipe"`
	// File is the asset's name beside the index.
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	// Built is when the build finished, RFC 3339.
	Built string `json:"built"`
}

// Index is what a source publishes: the packages it holds.
type Index struct {
	Version  int       `json:"version"`
	Packages []Package `json:"packages"`
}

var (
	fileRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,200}$`)
	sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)
	wordRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	idRe     = regexp.MustCompile(`^[0-9]{14}-[0-9a-f]{6}$`)
)

// valid reports whether a package's fields are shaped like what a CI job writes.
// The index is untrusted text: nothing in it reaches a path or a URL unchecked.
// The ID has the shape images.NewID makes; this package cannot import images.
func (p Package) valid() bool {
	return wordRe.MatchString(p.Product) && wordRe.MatchString(p.Release) &&
		wordRe.MatchString(p.Distro) && wordRe.MatchString(p.VersionID) &&
		wordRe.MatchString(p.Arch) && wordRe.MatchString(p.Recipe) &&
		idRe.MatchString(p.ID) &&
		fileRe.MatchString(p.File) && sha256Re.MatchString(p.SHA256) &&
		strings.HasPrefix(p.Root, "/") && !strings.ContainsAny(p.Root, "\x00\n") &&
		p.Size >= 0 && p.Size <= maxPackageBytes
}

// Want is what a host asks a source for.
type Want struct {
	Product string
	// Version is a series (1.30), whose newest published release is taken, or one
	// release (1.30.2), which is taken exactly.
	Version string
	Host    Host
	Arch    string
	// Root is the install root the host's paths are under.
	Root string
	// Recipe is the recipe the host would otherwise build from. A package made from
	// another recipe lacks something the host expects, or has something it does not.
	Recipe string
}

// Source is a place packages are published: a base URL holding index.json and
// the assets it names.
type Source struct {
	BaseURL string
	// Client defaults to one with a timeout that refuses a redirect off https.
	Client *http.Client
}

// CheckBaseURL accepts an https URL, or http only on a loopback address, which a
// test server uses. A package is unpacked as root, so it never travels in clear.
func CheckBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("prebuilt: %q is not a plain URL", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if loopback(u.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("prebuilt: %q must be an https URL", raw)
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s Source) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{
		Timeout: 30 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" && !loopback(req.URL.Hostname()) {
				return fmt.Errorf("redirect to %s refused: not https", req.URL.Redacted())
			}
			return nil
		},
	}
}

func (s Source) get(ctx context.Context, name string) (*http.Response, error) {
	if err := CheckBaseURL(s.BaseURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.BaseURL, "/")+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s is not published", ErrNoPackage, name)
		}
		return nil, fmt.Errorf("prebuilt: %s answered %s", name, resp.Status)
	}
	return resp, nil
}

// Find returns the newest package that fits w, or ErrNoPackage.
func (s Source) Find(ctx context.Context, w Want) (Package, error) {
	resp, err := s.get(ctx, "index.json")
	if err != nil {
		return Package{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var idx Index
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxIndexBytes)).Decode(&idx); err != nil {
		return Package{}, fmt.Errorf("prebuilt: read the index: %w", err)
	}
	if idx.Version != IndexVersion {
		return Package{}, fmt.Errorf("prebuilt: the index is version %d, this panel reads %d", idx.Version, IndexVersion)
	}
	var best Package
	found := false
	for _, p := range idx.Packages {
		if !p.valid() || !fits(p, w) {
			continue
		}
		if !found || newer(p, best) {
			best, found = p, true
		}
	}
	if !found {
		return Package{}, fmt.Errorf("%w: %s %s on %s %s %s", ErrNoPackage, w.Product, w.Version, w.Host.ID, w.Host.VersionID, w.Arch)
	}
	return best, nil
}

func fits(p Package, w Want) bool {
	return p.Product == w.Product && p.Distro == w.Host.ID && p.VersionID == w.Host.VersionID &&
		p.Arch == w.Arch && p.Root == w.Root && p.Recipe == w.Recipe &&
		ReleaseMatches(w.Version, p.Release)
}

// ReleaseMatches reports whether a published release satisfies a requested
// version: a series (two numbers) takes any release of it, a release (three)
// takes only itself.
func ReleaseMatches(version, release string) bool {
	if strings.Count(version, ".") >= 2 {
		return version == release
	}
	return strings.HasPrefix(release, version+".")
}

// newer reports whether a is a better pick than b: the higher release, then the
// later build.
func newer(a, b Package) bool {
	if c := compareRelease(a.Release, b.Release); c != 0 {
		return c > 0
	}
	if a.Built != b.Built {
		return a.Built > b.Built
	}
	return a.ID > b.ID
}

func compareRelease(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

// Fetch downloads p, checks it against the digest the index named, and unpacks it
// into versionsDir/<p.ID> with ExtractBuild's checks. Nothing is left behind on a
// failure: the download is a temporary file and the unpack is atomic.
func (s Source) Fetch(ctx context.Context, p Package, versionsDir string, required []string, progress docker.Progress) error {
	if !p.valid() {
		return fmt.Errorf("prebuilt: the package record for %s is malformed", p.File)
	}
	if err := os.MkdirAll(versionsDir, 0o755); err != nil {
		return err
	}
	say(progress, fmt.Sprintf("downloading %s (%d MB)", p.File, p.Size>>20))
	resp, err := s.get(ctx, p.File)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	tmp, err := os.CreateTemp(versionsDir, ".download-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	sum := sha256.New()
	// The body is wrapped so an install job shows the download as it goes; the
	// size is the index's, which is what the download must match anyway.
	body := jobs.ProgressReader(ctx, "downloading "+p.File, p.Size, resp.Body)
	n, err := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(body, maxPackageBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("prebuilt: download %s: %w", p.File, err)
	}
	if n > maxPackageBytes || (p.Size > 0 && n != p.Size) {
		return fmt.Errorf("prebuilt: %s is %d bytes, the index says %d", p.File, n, p.Size)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != p.SHA256 {
		return fmt.Errorf("prebuilt: %s has sha256 %s, the index says %s", p.File, got, p.SHA256)
	}
	say(progress, "sha256 matches; unpacking")

	f, err := os.Open(filepath.Clean(tmp.Name()))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("prebuilt: %s is not a gzip archive: %w", p.File, err)
	}
	defer func() { _ = gz.Close() }()
	return ExtractBuild(gz, versionsDir, p.ID, p.Product, required)
}
