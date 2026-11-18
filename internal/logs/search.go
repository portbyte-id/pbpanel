package logs

import (
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	// DefaultSearchMB and MaxSearchMB bound the window searched, counted in
	// uncompressed bytes from the newest end.
	DefaultSearchMB = 16
	MaxSearchMB     = 1024
	// DefaultSearchLimit and MaxSearchLimit bound the matches returned.
	DefaultSearchLimit = 1000
	MaxSearchLimit     = 10000

	maxPatternLen    = 1024
	dockerSearchTail = 100000
)

// Query is what to look for.
type Query struct {
	Pattern         string
	Regex           bool // RE2 syntax; otherwise Pattern is a literal
	CaseInsensitive bool
}

func (q Query) matcher() (func(string) bool, error) {
	if q.Pattern == "" {
		return nil, fmt.Errorf("%w: empty pattern", ErrBadQuery)
	}
	if len(q.Pattern) > maxPatternLen {
		return nil, fmt.Errorf("%w: pattern longer than %d bytes", ErrBadQuery, maxPatternLen)
	}
	if !q.Regex && !q.CaseInsensitive {
		p := q.Pattern
		return func(s string) bool { return strings.Contains(s, p) }, nil
	}
	expr := q.Pattern
	if !q.Regex {
		expr = regexp.QuoteMeta(expr)
	}
	if q.CaseInsensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadQuery, err)
	}
	return re.MatchString, nil
}

// SearchOptions bounds a search.
type SearchOptions struct {
	// LastMB is the window, in MB of uncompressed log counted back from the end.
	LastMB int
	// Limit is the maximum number of matches.
	Limit int

	lastBytes int64 // test override, takes precedence over LastMB
}

func (o SearchOptions) budget() int64 {
	if o.lastBytes > 0 {
		return o.lastBytes
	}
	mb := o.LastMB
	if mb <= 0 {
		mb = DefaultSearchMB
	}
	if mb > MaxSearchMB {
		mb = MaxSearchMB
	}
	return int64(mb) << 20
}

func (o SearchOptions) limit() int {
	switch {
	case o.Limit <= 0:
		return DefaultSearchLimit
	case o.Limit > MaxSearchLimit:
		return MaxSearchLimit
	}
	return o.Limit
}

// Match is one matching line. Results arrive oldest file first.
type Match struct {
	// File is the base name of the file (or the container) the line came from.
	File string `json:"file"`
	// Line is 1-based within the file. When Partial is true the file was only
	// scanned from the middle, and Line counts from the first scanned line.
	Line    int    `json:"line"`
	Partial bool   `json:"partial,omitempty"`
	Text    string `json:"text"`
}

// SearchStats describes what a search covered.
type SearchStats struct {
	Files        int   `json:"files"`
	BytesScanned int64 `json:"bytesScanned"`
	Matches      int   `json:"matches"`
	// LimitHit is true when the match limit stopped the search early.
	LimitHit bool `json:"limitHit"`
	// WindowCapped is true when older data lay beyond the MB window.
	WindowCapped bool `json:"windowCapped"`
}

// errStop ends a scan once the match limit is reached.
var errStop = errors.New("stop")

// Search resolves id and streams matching lines to emit, oldest first. File
// sources include their rotated siblings (name, name.1, name.2.gz, ...). Only
// the newest LastMB megabytes of uncompressed log are scanned.
func (r *Registry) Search(ctx context.Context, id string, q Query, opts SearchOptions, emit func(Match) error) (SearchStats, error) {
	var stats SearchStats
	match, err := q.matcher()
	if err != nil {
		return stats, err
	}
	src, err := r.Resolve(ctx, id)
	if err != nil {
		return stats, err
	}
	limit := opts.limit()
	check := func() error { return ctx.Err() }

	scan := func(name string, rd io.Reader, partial bool) error {
		n := 0
		return readLines(rd, func(line string) error {
			n++
			if n&0xff == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if !match(line) {
				return nil
			}
			if err := emit(Match{File: name, Line: n, Partial: partial, Text: line}); err != nil {
				return err
			}
			stats.Matches++
			if stats.Matches >= limit {
				stats.LimitHit = true
				return errStop
			}
			return nil
		}, check)
	}

	if src.Container != "" {
		if r.docker == nil {
			return stats, fmt.Errorf("%w: docker is not available", ErrUnavailable)
		}
		rc, err := r.docker.ContainerLogs(ctx, src.Container, DockerOptions{Tail: dockerSearchTail})
		if err != nil {
			return stats, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		defer func() { _ = rc.Close() }()
		stop := context.AfterFunc(ctx, func() { _ = rc.Close() })
		defer stop()
		cr := &countReader{r: io.LimitReader(rc, opts.budget()), n: &stats.BytesScanned}
		stats.Files = 1
		err = scan(src.Container, cr, false)
		return stats, searchErr(ctx, err)
	}

	files, err := rotatedFiles(src.Path)
	if err != nil {
		return stats, err
	}
	plan, capped := planWindow(files, opts.budget())
	stats.WindowCapped = capped
	for _, w := range plan {
		stats.Files++
		err := scanFile(w, &stats.BytesScanned, scan)
		if err != nil {
			return stats, searchErr(ctx, err)
		}
	}
	return stats, nil
}

func searchErr(ctx context.Context, err error) error {
	if errors.Is(err, errStop) {
		return nil
	}
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

type countReader struct {
	r io.Reader
	n *int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

// logFile is one member of a rotation set.
type logFile struct {
	path string
	idx  int // 0 is the live file, 1 the newest rotation, ...
	gz   bool
	size int64 // uncompressed bytes (the gzip trailer's figure for .gz)
}

type windowFile struct {
	logFile
	skip int64 // uncompressed bytes of this file that lie outside the window
}

// rotatedFiles lists the live file and its numbered siblings, newest first. A
// plain file wins over a .gz of the same index. Only regular files count, so a
// symlink dropped beside the log is never followed.
func rotatedFiles(base string) ([]logFile, error) {
	dir, name := filepath.Split(base)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, errors.Unwrap(err))
	}
	byIdx := map[int]logFile{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		idx, gz, ok := parseRotated(name, e.Name())
		if !ok {
			continue
		}
		if prev, dup := byIdx[idx]; dup && !prev.gz {
			continue
		}
		byIdx[idx] = logFile{path: filepath.Join(dir, e.Name()), idx: idx, gz: gz}
	}
	out := make([]logFile, 0, len(byIdx))
	for _, f := range byIdx {
		sz, err := uncompressedSize(f)
		if err != nil {
			continue // vanished or unreadable between listing and now
		}
		f.size = sz
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no such log", ErrUnavailable)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].idx < out[j].idx })
	return out, nil
}

// parseRotated recognises base, base.N and base.N.gz.
func parseRotated(base, name string) (idx int, gz, ok bool) {
	if name == base {
		return 0, false, true
	}
	rest, found := strings.CutPrefix(name, base+".")
	if !found {
		return 0, false, false
	}
	if r, isGz := strings.CutSuffix(rest, ".gz"); isGz {
		rest, gz = r, true
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 1 || strconv.Itoa(n) != rest {
		return 0, false, false
	}
	return n, gz, true
}

func uncompressedSize(f logFile) (int64, error) {
	st, err := os.Stat(f.path)
	if err != nil {
		return 0, err
	}
	if !f.gz {
		return st.Size(), nil
	}
	if st.Size() < 18 {
		return 0, nil
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = fh.Close() }()
	var tail [4]byte
	if _, err := fh.ReadAt(tail[:], st.Size()-4); err != nil {
		return 0, err
	}
	// ISIZE is the size modulo 2^32; larger logs are estimated as 4x compressed.
	isize := int64(binary.LittleEndian.Uint32(tail[:]))
	if isize == 0 {
		isize = st.Size() * 4
	}
	return isize, nil
}

// planWindow picks the newest files that fit budget bytes and returns them
// oldest first. The file that straddles the boundary is entered part way.
func planWindow(files []logFile, budget int64) (plan []windowFile, capped bool) {
	remaining := budget
	for i, f := range files {
		if remaining <= 0 {
			capped = true
			break
		}
		w := windowFile{logFile: f}
		if f.size > remaining {
			w.skip = f.size - remaining
			remaining = 0
			capped = capped || i < len(files)-1 || w.skip > 0
		} else {
			remaining -= f.size
		}
		plan = append(plan, w)
	}
	for i, j := 0, len(plan)-1; i < j; i, j = i+1, j-1 {
		plan[i], plan[j] = plan[j], plan[i]
	}
	return plan, capped
}

func scanFile(w windowFile, scanned *int64, scan func(name string, r io.Reader, partial bool) error) error {
	fh, err := os.Open(w.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // rotated away mid-search
		}
		return err
	}
	defer func() { _ = fh.Close() }()
	var rd io.Reader = fh
	var skipTo func(n int64) error
	skipTo = func(n int64) error { _, err := fh.Seek(n, io.SeekStart); return err }
	if w.gz {
		zr, err := gzip.NewReader(fh)
		if err != nil {
			return nil // truncated or half-written archive: nothing to read
		}
		defer func() { _ = zr.Close() }()
		rd = zr
		skipTo = func(n int64) error { _, err := io.CopyN(io.Discard, zr, n); return err }
	}
	partial := false
	if w.skip > 0 {
		if err := skipTo(w.skip - 1); err != nil {
			return nil
		}
		var b [1]byte
		if _, err := io.ReadFull(rd, b[:]); err != nil {
			return nil
		}
		if b[0] != '\n' {
			// Mid-line: discard the rest of that line.
			if err := discardLine(rd); err != nil {
				return nil
			}
		}
		partial = true
	}
	cr := &countReader{r: io.LimitReader(rd, w.size-w.skip), n: scanned}
	return scan(filepath.Base(w.path), cr, partial)
}

func discardLine(r io.Reader) error {
	var b [1]byte
	for {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return err
		}
		if b[0] == '\n' {
			return nil
		}
	}
}
