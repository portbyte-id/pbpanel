package update

import (
	"context"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/release"
)

// DefaultTTL is how long a successful check is reused. GitHub allows 60 anonymous
// API requests an hour per address, and a panel page is opened far more often
// than a release ships.
const DefaultTTL = 6 * time.Hour

// Info is what the UI shows about updates: the running version, the newest
// release, and what the release says has changed.
type Info struct {
	Current     string    `json:"current"`
	Latest      string    `json:"latest"`
	Available   bool      `json:"available"`
	Installable bool      `json:"installable"`
	Changelog   string    `json:"changelog"`
	ReleaseURL  string    `json:"release_url"`
	PublishedAt string    `json:"published_at"`
	BinarySize  int64     `json:"binary_size"`
	CheckedAt   time.Time `json:"checked_at"`
	// Error is why the check failed. The current version is still reported, so
	// the footer can show it when GitHub cannot be reached.
	Error string `json:"error,omitempty"`
}

// Checker asks GitHub for the newest release and caches a good answer.
type Checker struct {
	// Current is the running version, as internal/version reports it.
	Current string
	// Repo is owner/name; empty means release.DefaultRepo.
	Repo string
	// Token is optional; the repository may be private.
	Token string
	// BaseURL overrides the GitHub API root. Tests point it at a fake.
	BaseURL string
	// TTL is how long a successful check is reused. Zero means DefaultTTL.
	TTL time.Duration
	// Now is the clock. Nil means time.Now.
	Now func() time.Time

	mu   sync.Mutex
	last Info
	have bool
}

// Info returns the update state. force bypasses the cache. A failed check is
// returned in Info.Error and is not cached, so the next request tries again.
func (c *Checker) Info(ctx context.Context, force bool) Info {
	now := c.now()

	c.mu.Lock()
	ttl := c.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if c.have && !force && now.Sub(c.last.CheckedAt) < ttl {
		info := c.last
		c.mu.Unlock()
		return info
	}
	c.mu.Unlock()

	info := Info{Current: c.Current, CheckedAt: now}
	rc := &release.ReleaseClient{Repo: c.Repo, Token: c.Token, Version: "latest", BaseURL: c.BaseURL}
	meta, err := rc.Meta(ctx)
	if err != nil {
		info.Error = err.Error()
		return info
	}

	info.Latest = meta.Tag
	info.Available = meta.Tag != "" && meta.Tag != c.Current
	info.Installable = meta.Installable
	info.Changelog = meta.Notes
	info.ReleaseURL = meta.URL
	info.PublishedAt = meta.PublishedAt
	info.BinarySize = meta.BinarySize

	c.mu.Lock()
	c.last, c.have = info, true
	c.mu.Unlock()
	return info
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
