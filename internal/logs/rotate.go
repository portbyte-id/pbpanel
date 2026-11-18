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
	// Keep is the number of rotated files kept per log (name.log.1 .. .N).
	Keep int
	// Compress gzips rotated files once their writers have reopened.
	Compress bool
}

// RotateReport summarises one Rotate pass.
type RotateReport struct {
	Rotated    []string `json:"rotated"`
	Removed    int      `json:"removed"`
	Compressed int      `json:"compressed"`
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
	}
	cfg.Root = filepath.Clean(cfg.Root)
	return &Rotator{cfg: cfg, hooks: hooks}, nil
}

// Rotate performs one pass. A missing root, or a log that vanishes mid-pass, is
// not an error. Failures on one file do not stop the others; they are joined in
// the returned error. Reopen hooks run once, only if something was rotated, and
// compression of the newest rotation is skipped if any hook failed because its
// writer may still hold that file open.
func (r *Rotator) Rotate(ctx context.Context) (RotateReport, error) {
	var rep RotateReport
	var errs []error
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
		if info, err := d.Info(); err == nil && info.Size() >= r.cfg.MaxBytes {
			live = append(live, p)
		}
		return nil
	})
	if err != nil {
		return rep, err
	}

	for _, p := range live {
		removed, err := r.shift(p)
		rep.Removed += removed
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rep.Rotated = append(rep.Rotated, p)
	}
	if len(rep.Rotated) == 0 {
		return rep, errors.Join(errs...)
	}

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
	return rep, errors.Join(errs...)
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
// the live file to .1. Missing slots are skipped.
func (r *Rotator) shift(p string) (removed int, err error) {
	for _, suffix := range []string{"", ".gz"} {
		oldest := p + "." + strconv.Itoa(r.cfg.Keep) + suffix
		if rerr := os.Remove(oldest); rerr == nil {
			removed++
		} else if !errors.Is(rerr, fs.ErrNotExist) {
			return removed, rerr
		}
	}
	for i := r.cfg.Keep - 1; i >= 1; i-- {
		for _, suffix := range []string{"", ".gz"} {
			from := p + "." + strconv.Itoa(i) + suffix
			to := p + "." + strconv.Itoa(i+1) + suffix
			if rerr := os.Rename(from, to); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				return removed, rerr
			}
		}
	}
	if rerr := os.Rename(p, p+".1"); rerr != nil {
		if errors.Is(rerr, fs.ErrNotExist) {
			return removed, nil
		}
		return removed, rerr
	}
	return removed, nil
}

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
