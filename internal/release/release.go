// Package release fetches the panel's GitHub releases: the metadata the update
// check shows, and the checksum-verified binary an upgrade installs. It depends
// on the standard library alone, so the HTTP layer can use it without pulling in
// the installer.
package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// DefaultRepo is the GitHub repository `pbp upgrade` pulls releases from.
	// PB_REPO overrides it, as it does for install.sh.
	DefaultRepo = "portbyte-id/pbpanel"

	githubAPI = "https://api.github.com"

	// maxSumsBytes bounds the SHA256SUMS download; the file is a few lines.
	maxSumsBytes = 1 << 20
)

// ErrNoRelease means the repository has no (matching) release to upgrade to.
var ErrNoRelease = errors.New("no release found")

// Release is a downloaded, checksum-verified release binary.
type Release struct {
	// Tag is the release's tag name, for example v0.2.0.
	Tag string
	// Path is the verified binary, in a temporary directory the caller removes
	// through Cleanup.
	Path string
	// Cleanup removes the temporary directory holding Path.
	Cleanup func()
}

// ReleaseClient fetches release binaries from the GitHub API.
type ReleaseClient struct {
	Repo    string // owner/name; empty means DefaultRepo
	Token   string // optional; the repository may be private
	Version string // "latest" or a tag; empty means latest
	BaseURL string // API root; empty means https://api.github.com
	HTTP    *http.Client
	Out     io.Writer
	// Arch is the GOARCH to fetch; empty means the running one.
	Arch string
	// Progress, when set, is told how many bytes of the binary have arrived and
	// how many it is expected to be (0 when the server did not say). It is called
	// from the download goroutine, after each read.
	Progress func(done, total int64)
}

type ghRelease struct {
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt string    `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// ReleaseMeta describes a release without downloading its binary: what the
// update check shows and what the upgrade needs to know before it starts.
type ReleaseMeta struct {
	Tag         string
	Name        string
	Notes       string // the release body, Markdown as GitHub stores it
	URL         string // the release page
	PublishedAt string // RFC 3339, as GitHub reports it
	// BinarySize is the size of the binary asset for the client's arch, or 0
	// when the release does not publish one.
	BinarySize int64
	// Installable reports that the release ships the binary for this arch and a
	// checksum file, which is all Fetch needs.
	Installable bool
}

func (c *ReleaseClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (c *ReleaseClient) get(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return c.client().Do(req)
}

// resolve looks up the release without downloading its binary.
func (c *ReleaseClient) resolve(ctx context.Context) (ghRelease, error) {
	repo := c.Repo
	if repo == "" {
		repo = DefaultRepo
	}
	base := c.BaseURL
	if base == "" {
		base = githubAPI
	}
	url := base + "/repos/" + repo + "/releases/latest"
	if c.Version != "" && c.Version != "latest" {
		url = base + "/repos/" + repo + "/releases/tags/" + c.Version
	}

	resp, err := c.get(ctx, url, "application/vnd.github+json")
	if err != nil {
		return ghRelease{}, fmt.Errorf("could not reach GitHub: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return ghRelease{}, fmt.Errorf("%w in %s (if it is private, set PB_GITHUB_TOKEN)", ErrNoRelease, repo)
	case http.StatusUnauthorized, http.StatusForbidden:
		return ghRelease{}, fmt.Errorf("GitHub refused the request (%d); check that the token can read %s", resp.StatusCode, repo)
	default:
		return ghRelease{}, fmt.Errorf("GitHub answered %d for %s", resp.StatusCode, url)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return ghRelease{}, fmt.Errorf("decode the release: %w", err)
	}
	if rel.Tag == "" {
		return ghRelease{}, errors.New("the release response has no tag_name")
	}
	return rel, nil
}

// LatestTag returns the tag the client would download.
func (c *ReleaseClient) LatestTag(ctx context.Context) (string, error) {
	rel, err := c.resolve(ctx)
	return rel.Tag, err
}

// Meta describes the release the client would download, without downloading it.
func (c *ReleaseClient) Meta(ctx context.Context) (ReleaseMeta, error) {
	rel, err := c.resolve(ctx)
	if err != nil {
		return ReleaseMeta{}, err
	}
	meta := ReleaseMeta{
		Tag:         rel.Tag,
		Name:        rel.Name,
		Notes:       rel.Body,
		URL:         rel.HTMLURL,
		PublishedAt: rel.PublishedAt,
	}
	if bin, _, err := c.assetsOf(rel); err == nil {
		meta.Installable = true
		meta.BinarySize = bin.Size
	}
	return meta, nil
}

// assetsOf finds the binary and checksum assets of rel for the client's arch.
func (c *ReleaseClient) assetsOf(rel ghRelease) (ghAsset, ghAsset, error) {
	arch := c.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	binName := "pbp-linux-" + arch

	var bin, sum ghAsset
	var haveBin, haveSum bool
	for _, a := range rel.Assets {
		switch a.Name {
		case binName:
			bin, haveBin = a, true
		case "SHA256SUMS":
			sum, haveSum = a, true
		}
	}
	if !haveBin {
		return bin, sum, fmt.Errorf("release %s has no %s asset", rel.Tag, binName)
	}
	if !haveSum {
		return bin, sum, fmt.Errorf("release %s has no SHA256SUMS asset; refusing to install an unverified binary", rel.Tag)
	}
	return bin, sum, nil
}

// Fetch downloads pbp-linux-<arch> and verifies it against SHA256SUMS. A release
// without a checksum file is refused rather than installed unverified.
func (c *ReleaseClient) Fetch(ctx context.Context) (*Release, error) {
	rel, err := c.resolve(ctx)
	if err != nil {
		return nil, err
	}
	arch := c.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	binName := "pbp-linux-" + arch

	binAsset, sumAsset, err := c.assetsOf(rel)
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", "pbp-upgrade-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	if _, err := c.download(ctx, sumAsset.URL, filepath.Join(dir, "SHA256SUMS"), maxSumsBytes, nil); err != nil {
		cleanup()
		return nil, fmt.Errorf("download SHA256SUMS: %w", err)
	}
	want, err := sumFor(filepath.Join(dir, "SHA256SUMS"), binName)
	if err != nil {
		cleanup()
		return nil, err
	}

	path := filepath.Join(dir, "pbp")
	got, err := c.download(ctx, binAsset.URL, path, 0, c.Progress)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("download %s: %w", binName, err)
	}
	if !strings.EqualFold(got, want) {
		cleanup()
		return nil, fmt.Errorf("checksum mismatch for %s (expected %s, got %s)", binName, want, got)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		cleanup()
		return nil, err
	}
	return &Release{Tag: rel.Tag, Path: path, Cleanup: cleanup}, nil
}

// download streams an asset to dst and returns its sha256. limit 0 means no cap.
// progress, when set, is called after each chunk with the bytes so far and the
// Content-Length the server sent (0 when it sent none).
func (c *ReleaseClient) download(ctx context.Context, url, dst string, limit int64, progress func(done, total int64)) (string, error) {
	resp, err := c.get(ctx, url, "application/octet-stream")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("answered %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	var r io.Reader = resp.Body
	if limit > 0 {
		r = io.LimitReader(resp.Body, limit)
	}
	if progress != nil {
		r = &progressReader{r: r, total: resp.ContentLength, report: progress}
	}
	if _, err := io.Copy(io.MultiWriter(f, h), r); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// progressReader counts the bytes read through it and reports them.
type progressReader struct {
	r      io.Reader
	done   int64
	total  int64
	report func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		p.report(p.done, p.total)
	}
	return n, err
}

// sumFor reads the digest recorded for name in a sha256sum-format file.
func sumFor(path, name string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS has no entry for %s", name)
}
