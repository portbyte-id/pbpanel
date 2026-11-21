package logs

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ReopenHook asks a process that writes a rotated log to reopen its file
// (nginx `-s reopen`, an FPM reopen signal, the panel's own writers).
type ReopenHook func(ctx context.Context) error

// RotateConfig is the rotation policy for every `*.log` under Root.
type RotateConfig struct {
	// Root is the directory tree rotated, normally /www/panel/logs. Only
	// regular files below it are touched; symlinks are never followed.
	Root string
	// MaxBytes rotates a file once it is at least this large.
	MaxBytes int64
	// MaxAge rotates a live file once the time of its last write — its mtime —
	// is older than this, so a log that stays small is still cut on a schedule.
	// Zero turns the age trigger off, which is the size-only behaviour.
	//
	// mtime is the trigger because the rotator does not open or track the files
	// it watches, so it has no other evidence of age; it is also what the writer
	// itself updates on every append. The consequence is deliberate and worth
	// knowing: a log that is still being appended to has a fresh mtime and is
	// never age-rotated, so this cuts the logs that went quiet (an app that was
	// retired, a site whose traffic stopped) and never a live one mid-write.
	// A busy log is bounded by MaxBytes and Keep instead.
	MaxAge time.Duration
	// Keep is the number of rotated files kept per log (name.log.1 .. .N).
	Keep int
	// RetainAge deletes a rotated file (name.log.N or name.log.N.gz) once it has
	// been a rotated file for longer than this, whatever Keep says. Zero keeps
	// the count-only retention.
	//
	// A rotated file's mtime is set to the moment of rotation, not to that of its
	// last write, so this counts from when the file was cut and a plain `.1` and
	// its later `.gz` agree on their age. Without that, a file written days
	// before it was rotated would be deleted the pass after it rotated.
	RetainAge time.Duration
	// Compress gzips rotated files once their writers have reopened.
	Compress bool
}

// RotateReport summarises one Rotate pass.
type RotateReport struct {
	Rotated    []string `json:"rotated"`
	Removed    int      `json:"removed"`
	Compressed int      `json:"compressed"`
	// Pruned lists the rotated files RetainAge deleted.
	Pruned []string `json:"pruned"`
}

// Rotator rotates logs in place. It does not create the fresh live file: the
// writer's reopen creates it with the right owner and mode.
type Rotator struct {
	cfg   RotateConfig
	hooks []ReopenHook
}

// NewRotator validates cfg. hooks run, in order, after files were renamed.
func NewRotator(cfg RotateConfig, hooks ...ReopenHook) (*Rotator, error) {
	switch {
	case cfg.Root == "":
		return nil, errors.New("logs: rotation root is required")
	case cfg.MaxBytes <= 0:
		return nil, errors.New("logs: rotation size threshold must be positive")
	case cfg.Keep < 1:
		return nil, errors.New("logs: rotation keep count must be at least 1")
	case cfg.MaxAge < 0:
		return nil, errors.New("logs: rotation age threshold must not be negative")
	case cfg.RetainAge < 0:
		return nil, errors.New("logs: rotation retention must not be negative")
	}
	cfg.Root = filepath.Clean(cfg.Root)
	return &Rotator{cfg: cfg, hooks: hooks}, nil
}

// Rotate performs one pass. A missing root, or a log that vanishes mid-pass, is
// not an error. Failures on one file do not stop the others; they are joined in
// the returned error. Reopen hooks run once, only if something was rotated, and
// compression of the newest rotation is skipped if any hook failed because its
// writer may still hold that file open.
//
// A live file is rotated when it is at least MaxBytes or, when MaxAge is set,
// when its last write is older than MaxAge. The two are independent: either one
// triggers. Pruning by RetainAge runs after the hooks, on files that are already
// rotated, and never touches a live log.
func (r *Rotator) Rotate(ctx context.Context) (RotateReport, error) {
	var rep RotateReport
	var errs []error
	now := time.Now()
	var live []string
	err := filepath.WalkDir(r.cfg.Root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if errors.Is(werr, fs.ErrNotExist) {
				return nil
			}
			errs = append(errs, werr)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".log") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() >= r.cfg.MaxBytes {
			live = append(live, p)
			return nil
		}
		if r.cfg.MaxAge > 0 && r.stale(info, now) {
			live = append(live, p)
		}
		return nil
	})
	if err != nil {
		return rep, err
	}

	for _, p := range live {
		removed, moved, err := r.shift(p, now)
		rep.Removed += removed
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if moved {
			rep.Rotated = append(rep.Rotated, p)
		}
	}
	hooksRan := len(rep.Rotated) > 0
	if hooksRan {
		hooksOK := true
		for _, h := range r.hooks {
			if err := h(ctx); err != nil {
				hooksOK = false
				errs = append(errs, fmt.Errorf("logs: reopen hook: %w", err))
			}
		}
		if r.cfg.Compress {
			first := 1
			if !hooksOK {
				first = 2
			}
			for _, p := range rep.Rotated {
				for i := first; i <= r.cfg.Keep; i++ {
					src := p + "." + strconv.Itoa(i)
					done, err := gzipFile(src)
					if err != nil {
						errs = append(errs, err)
					}
					if done {
						rep.Compressed++
					}
				}
			}
		}
	}

	if r.cfg.RetainAge > 0 {
		pruned, err := r.prune(now)
		rep.Pruned = pruned
		if err != nil {
			errs = append(errs, err)
		}
	}
	return rep, errors.Join(errs...)
}

// stale reports whether a file's last write is older than MaxAge. A file whose
// mtime is in the future (a clock step) is not stale: rotating it would be a
// guess.
func (r *Rotator) stale(info fs.FileInfo, now time.Time) bool {
	return now.Sub(info.ModTime()) > r.cfg.MaxAge
}

// Run rotates every interval until ctx ends. onError, if non-nil, receives the
// error of any pass that had one.
func (r *Rotator) Run(ctx context.Context, interval time.Duration, onError func(error)) {
	if interval <= 0 {
		interval = time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := r.Rotate(ctx); err != nil && onError != nil {
				onError(err)
			}
		}
	}
}

// shift drops the oldest rotation, moves every other one up a slot and renames
// the live file to .1. Missing slots are skipped; moved reports whether the live
// file was one of them, which is false when it vanished mid-pass.
//
// The renamed file's mtime is set to now, so RetainAge counts from the moment of
// rotation rather than from the last write of the content it holds.
func (r *Rotator) shift(p string, now time.Time) (removed int, moved bool, err error) {
	for _, suffix := range []string{"", ".gz"} {
		oldest := p + "." + strconv.Itoa(r.cfg.Keep) + suffix
		if rerr := os.Remove(oldest); rerr == nil {
			removed++
		} else if !errors.Is(rerr, fs.ErrNotExist) {
			return removed, false, rerr
		}
	}
	for i := r.cfg.Keep - 1; i >= 1; i-- {
		for _, suffix := range []string{"", ".gz"} {
			from := p + "." + strconv.Itoa(i) + suffix
			to := p + "." + strconv.Itoa(i+1) + suffix
			if rerr := os.Rename(from, to); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				return removed, false, rerr
			}
		}
	}
	if rerr := os.Rename(p, p+".1"); rerr != nil {
		if errors.Is(rerr, fs.ErrNotExist) {
			return removed, false, nil
		}
		return removed, false, rerr
	}
	// A failed stamp is not a failed rotation: the file moved, and the mtime is
	// only what RetainAge measures from. Best effort, deliberately unchecked.
	_ = os.Chtimes(p+".1", now, now)
	return removed, true, nil
}

// prune deletes rotated files whose mtime is older than RetainAge. Only a
// `name.log.<n>` or `name.log.<n>.gz` under Root is a candidate, and a live log
// (`name.log`) is never one, so this cannot delete a file a writer holds open.
func (r *Rotator) prune(now time.Time) ([]string, error) {
	var pruned []string
	var errs []error
	err := filepath.WalkDir(r.cfg.Root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if !errors.Is(werr, fs.ErrNotExist) {
				errs = append(errs, werr)
			}
			return nil
		}
		if !d.Type().IsRegular() || !isRotatedPath(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if now.Sub(info.ModTime()) <= r.cfg.RetainAge {
			return nil
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			return nil
		}
		pruned = append(pruned, p)
		return nil
	})
	if err != nil {
		return pruned, err
	}
	return pruned, errors.Join(errs...)
}

// rotatedPattern is a rotated log's name: the live name, a slot number, and an
// optional .gz. The slot is 1..N, so `app.log.20261101` (an app's own
// timestamped file) is not a rotated one.
var rotatedPattern = regexp.MustCompile(`^.+\.log\.[1-9][0-9]{0,3}(\.gz)?$`)

func isRotatedPath(name string) bool { return rotatedPattern.MatchString(name) }

// gzipFile compresses src to src+".gz" via a temp file and removes src. It
// reports false, nil when src does not exist.
func gzipFile(src string) (bool, error) {
	in, err := os.Open(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = in.Close() }()
	st, err := in.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return false, err
	}
	tmp := src + ".gz.tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return false, err
	}
	zw := gzip.NewWriter(out)
	_, err = io.Copy(zw, in)
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if serr := out.Sync(); err == nil {
		err = serr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, src+".gz")
	}
	if err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	_ = in.Close()
	if err := os.Remove(src); err != nil {
		return true, err
	}
	return true, nil
}
