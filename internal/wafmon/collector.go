package wafmon

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Limits. Everything the collector keeps is bounded by these, whatever traffic
// or log volume it sees.
const (
	// Step is the width of one bucket.
	Step = 10 * time.Second
	// Retention is how far back buckets are kept.
	Retention = time.Hour
	// RingSize is how many recent requests, and how many events, are kept.
	RingSize = 2000

	// keysPerBucket caps each distinct-key map of a bucket (IPs, paths, agents).
	// A scan from thousands of addresses drops the surplus from the top lists
	// but still counts it in the totals.
	keysPerBucket = 48

	accessBackfill = 1 << 20
	auditBackfill  = 4 << 20
	// maxReadPerPoll bounds the bytes one file contributes to one poll, so a log
	// that grew by gigabytes is caught up over several ticks.
	maxReadPerPoll = 4 << 20

	pathCap  = 200
	agentCap = 200

	accessSuffix = ".access.log"
	auditSuffix  = ".waf-audit.log"
)

var domainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)

// ValidDomain reports whether s can be a site's domain as the monitor knows it:
// lowercase LDH labels. It exists so a query parameter can never be a path.
func ValidDomain(s string) bool { return domainPattern.MatchString(s) }

// Options configures a Collector.
type Options struct {
	// Dir holds the per-site logs (nginx.Paths.SiteLogDir).
	Dir string
	// Now defaults to time.Now.
	Now    func() time.Time
	Logger *slog.Logger
}

type ipStat struct {
	req, errs int
	bytes     int64
}

type pathStat struct{ req, errs int }

type bucket struct {
	req, s2, s3, s4, s5 int
	bytes               int64
	rtSum, rtMax        float64
	rtN                 int
	waf                 int
	ips                 map[string]*ipStat
	paths               map[string]*pathStat
	agents              map[string]int
	statuses            map[int]int
}

func (b *bucket) add(r Request) {
	b.req++
	switch r.Status / 100 {
	case 2:
		b.s2++
	case 3:
		b.s3++
	case 4:
		b.s4++
	case 5:
		b.s5++
	}
	b.bytes += r.Bytes
	if r.hasMS {
		b.rtSum += r.MS
		b.rtN++
		b.rtMax = max(b.rtMax, r.MS)
	}
	isErr := r.Status >= 400

	if b.ips == nil {
		b.ips = map[string]*ipStat{}
		b.paths = map[string]*pathStat{}
		b.agents = map[string]int{}
		b.statuses = map[int]int{}
	}
	if s, ok := b.ips[r.IP]; ok || len(b.ips) < keysPerBucket {
		if !ok {
			s = &ipStat{}
			b.ips[r.IP] = s
		}
		s.req++
		s.bytes += r.Bytes
		if isErr {
			s.errs++
		}
	}
	p := pathOnly(r.Path)
	if s, ok := b.paths[p]; ok || len(b.paths) < keysPerBucket {
		if !ok {
			s = &pathStat{}
			b.paths[p] = s
		}
		s.req++
		if isErr {
			s.errs++
		}
	}
	a := clip(r.Agent, agentCap)
	if _, ok := b.agents[a]; ok || len(b.agents) < keysPerBucket {
		b.agents[a]++
	}
	if _, ok := b.statuses[r.Status]; ok || len(b.statuses) < keysPerBucket {
		b.statuses[r.Status]++
	}
}

// pathOnly drops the query string: it is where tokens live and where every
// distinct value would otherwise be its own top-list key.
func pathOnly(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	return clip(p, pathCap)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

type siteData struct {
	buckets map[int64]*bucket
}

// ring keeps the newest RingSize values.
type ring[T any] struct {
	buf  []T
	next int
}

func (r *ring[T]) push(v T) {
	if len(r.buf) < RingSize {
		r.buf = append(r.buf, v)
		return
	}
	r.buf[r.next] = v
	r.next = (r.next + 1) % RingSize
}

// newestFirst calls fn from the newest value to the oldest until it returns false.
func (r *ring[T]) newestFirst(fn func(T) bool) {
	n := len(r.buf)
	for i := range n {
		// next is the oldest slot once the ring is full, and 0 before.
		idx := (r.next - 1 - i + 2*n) % n
		if len(r.buf) < RingSize {
			idx = n - 1 - i
		}
		if !fn(r.buf[idx]) {
			return
		}
	}
}

// Collector tails the site logs. Poll is safe to call from one goroutine at a
// time; the query methods are safe to call at any time.
type Collector struct {
	dir string
	now func() time.Time
	log *slog.Logger

	pollMu sync.Mutex
	tails  map[string]*tail

	mu     sync.RWMutex
	sites  map[string]*siteData
	reqs   ring[Request]
	events ring[Event]
	reqSeq uint64
	evSeq  uint64
}

// New returns a Collector for the logs in o.Dir.
func New(o Options) *Collector {
	c := &Collector{
		dir: o.Dir, now: o.Now, log: o.Logger,
		tails: map[string]*tail{}, sites: map[string]*siteData{},
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	return c
}

// Run polls once a second until ctx ends.
func (c *Collector) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		c.Poll()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type tail struct {
	domain string
	audit  bool
	info   os.FileInfo
	offset int64
	// started is false until the first read, which backfills only the end of a
	// large file.
	started bool
	// skip drops input up to the next newline: after a backfill that began in
	// the middle of a line, and after a line longer than MaxLine.
	skip    bool
	pending []byte
}

// read returns the complete lines written since the last call.
func (t *tail) read(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// A different file (rotation renamed the old one away) or a shorter one
	// (truncated in place) starts again from the top.
	if (t.info != nil && !os.SameFile(t.info, fi)) || fi.Size() < t.offset {
		t.offset, t.pending, t.skip = 0, nil, false
	}
	t.info = fi
	if !t.started {
		t.started = true
		back := int64(accessBackfill)
		if t.audit {
			back = auditBackfill
		}
		if fi.Size() > back {
			t.offset, t.skip = fi.Size()-back, true
		}
	}
	n := min(fi.Size()-t.offset, maxReadPerPoll)
	if n <= 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, t.offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	t.offset += int64(got)
	return t.split(buf[:got]), nil
}

func (t *tail) split(data []byte) []string {
	var lines []string
	for len(data) > 0 {
		i := indexNL(data)
		if i < 0 {
			if t.skip {
				return lines
			}
			if len(t.pending)+len(data) > MaxLine {
				t.pending, t.skip = nil, true
				return lines
			}
			t.pending = append(t.pending, data...)
			return lines
		}
		chunk := data[:i]
		data = data[i+1:]
		if t.skip {
			t.skip = false
			t.pending = nil
			continue
		}
		if len(t.pending)+len(chunk) <= MaxLine {
			line := string(append(t.pending, chunk...))
			lines = append(lines, strings.TrimSuffix(line, "\r"))
		}
		t.pending = nil
	}
	return lines
}

func indexNL(p []byte) int {
	for i, b := range p {
		if b == '\n' {
			return i
		}
	}
	return -1
}

// Poll reads what was appended to every site log since the last poll.
func (c *Collector) Poll() {
	c.pollMu.Lock()
	defer c.pollMu.Unlock()

	entries, err := os.ReadDir(c.dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		c.log.Warn("waf monitor: list the site log directory", "dir", c.dir, "error", err)
	}
	seen := map[string]bool{}
	live := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		var domain string
		var audit bool
		switch {
		case strings.HasSuffix(name, accessSuffix):
			domain = strings.TrimSuffix(name, accessSuffix)
		case strings.HasSuffix(name, auditSuffix):
			domain, audit = strings.TrimSuffix(name, auditSuffix), true
		default:
			continue
		}
		if !ValidDomain(domain) || !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(c.dir, name)
		seen[path], live[domain] = true, true
		t := c.tails[path]
		if t == nil {
			t = &tail{domain: domain, audit: audit}
			c.tails[path] = t
		}
		lines, err := t.read(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				c.log.Warn("waf monitor: read a log", "file", name, "error", err)
			}
			continue
		}
		c.ingest(domain, audit, lines)
	}
	for path := range c.tails {
		if !seen[path] {
			delete(c.tails, path)
		}
	}

	c.mu.Lock()
	for d := range c.sites {
		if !live[d] {
			delete(c.sites, d)
		}
	}
	horizon := c.now().Add(-Retention).Unix() / int64(Step.Seconds())
	for _, s := range c.sites {
		for k := range s.buckets {
			if k < horizon {
				delete(s.buckets, k)
			}
		}
	}
	c.mu.Unlock()
}

func (c *Collector) bucketFor(domain string, at time.Time) *bucket {
	s := c.sites[domain]
	if s == nil {
		s = &siteData{buckets: map[int64]*bucket{}}
		c.sites[domain] = s
	}
	k := at.Unix() / int64(Step.Seconds())
	b := s.buckets[k]
	if b == nil {
		b = &bucket{}
		s.buckets[k] = b
	}
	return b
}

func (c *Collector) ingest(domain string, audit bool, lines []string) {
	if len(lines) == 0 {
		return
	}
	now := c.now()
	horizon := now.Add(-Retention)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range lines {
		if audit {
			e, ok := ParseAudit(l)
			if !ok {
				continue
			}
			if e.Time.IsZero() {
				e.Time = now
			}
			e.Site = domain
			c.evSeq++
			e.Seq = c.evSeq
			c.events.push(e)
			if !e.Time.Before(horizon) {
				c.bucketFor(domain, e.Time).waf++
			}
			continue
		}
		r, ok := ParseAccess(l)
		if !ok {
			continue
		}
		if r.Time.IsZero() {
			r.Time = now
		}
		r.Site = domain
		c.reqSeq++
		r.Seq = c.reqSeq
		c.reqs.push(r)
		if !r.Time.Before(horizon) {
			c.bucketFor(domain, r.Time).add(r)
		}
	}
}
