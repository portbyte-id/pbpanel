package logs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	// DefaultTailLines is the initial backlog when TailOptions.Lines is zero.
	DefaultTailLines = 200
	// MaxTailLines caps the initial backlog.
	MaxTailLines = 5000

	defaultPoll    = 500 * time.Millisecond
	maxBacklogRead = 8 << 20
)

// TailOptions controls Tail.
type TailOptions struct {
	// Lines is the number of trailing lines to emit first.
	Lines int
	// Follow keeps streaming new lines until the context ends.
	Follow bool
	// PollInterval is how often a followed file is checked for growth,
	// truncation and rotation. Zero means 500ms.
	PollInterval time.Duration
}

func (o TailOptions) normalized() TailOptions {
	if o.Lines <= 0 {
		o.Lines = DefaultTailLines
	}
	if o.Lines > MaxTailLines {
		o.Lines = MaxTailLines
	}
	if o.PollInterval <= 0 {
		o.PollInterval = defaultPoll
	}
	return o
}

// Tail resolves id and emits its trailing lines, then (with Follow) every new
// line. It returns nil at the end of a non-followed log, ctx.Err() when the
// context ends, and the emit error if emit fails. A followed file that is
// truncated or replaced by rotation is reopened from its start, after the old
// file is drained, so no line is skipped.
func (r *Registry) Tail(ctx context.Context, id string, opts TailOptions, emit func(line string) error) error {
	src, err := r.Resolve(ctx, id)
	if err != nil {
		return err
	}
	opts = opts.normalized()
	if src.Container != "" {
		return r.tailDocker(ctx, src.Container, opts, emit)
	}
	return tailFile(ctx, src.Path, opts, emit)
}

// TailChan is Tail for callers that prefer channels. Lines is closed when the
// tail ends; the single value on errc (nil, or the reason) is sent before that.
func (r *Registry) TailChan(ctx context.Context, id string, opts TailOptions) (<-chan string, <-chan error) {
	lines := make(chan string, 64)
	errc := make(chan error, 1)
	go func() {
		err := r.Tail(ctx, id, opts, func(l string) error {
			select {
			case lines <- l:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		errc <- err
		close(errc)
		close(lines)
	}()
	return lines, errc
}

func (r *Registry) tailDocker(ctx context.Context, name string, opts TailOptions, emit func(string) error) error {
	if r.docker == nil {
		return fmt.Errorf("%w: docker is not available", ErrUnavailable)
	}
	rc, err := r.docker.ContainerLogs(ctx, name, DockerOptions{Tail: opts.Lines, Follow: opts.Follow})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer rc.Close()
	stop := context.AfterFunc(ctx, func() { rc.Close() })
	defer stop()
	err = readLines(rc, emit, nil)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func tailFile(ctx context.Context, path string, opts TailOptions, emit func(string) error) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, errors.Unwrap(err))
	}
	defer func() { f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	lines, partial, rawLen, err := lastLines(f, info.Size(), opts.Lines)
	if err != nil {
		return err
	}
	for _, l := range lines {
		if err := emit(l); err != nil {
			return err
		}
	}
	if !opts.Follow {
		if partial != "" {
			return emit(partial)
		}
		return nil
	}
	// Resume right after the last complete line; the partial one is re-read.
	if _, err := f.Seek(info.Size()-int64(rawLen), io.SeekStart); err != nil {
		return err
	}

	var sp splitter
	buf := make([]byte, 64<<10)
	drain := func() error {
		for {
			n, err := f.Read(buf)
			if n > 0 {
				if e := sp.feed(buf[:n], emit); e != nil {
					return e
				}
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	tick := time.NewTicker(opts.PollInterval)
	defer tick.Stop()
	for {
		// Stat before draining: whatever the old descriptor holds is read first,
		// and only then is a replaced file swapped in.
		st, statErr := os.Stat(path)
		if err := drain(); err != nil {
			return err
		}
		switch {
		case statErr == nil && !os.SameFile(info, st):
			if err := sp.flush(emit); err != nil {
				return err
			}
			if nf, err := os.Open(path); err == nil {
				ni, serr := nf.Stat()
				if serr == nil {
					f.Close()
					f, info = nf, ni
					continue
				}
				nf.Close()
			}
		case statErr == nil:
			// Size comes from the open descriptor, not the stat taken before the
			// drain, which can be stale by the bytes the drain just read.
			fi, ferr := f.Stat()
			if pos, err := f.Seek(0, io.SeekCurrent); err == nil && ferr == nil && fi.Size() < pos {
				if err := sp.flush(emit); err != nil {
					return err
				}
				if _, err := f.Seek(0, io.SeekStart); err != nil {
					return err
				}
				continue
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// lastLines returns up to n complete trailing lines of the first size bytes of
// f, plus the unterminated tail of the file if there is one.
func lastLines(f *os.File, size int64, n int) (lines []string, partial string, rawLen int, err error) {
	var data []byte
	start := size
	for start > 0 && int64(len(data)) < maxBacklogRead && bytes.Count(data, []byte{'\n'}) <= n {
		step := int64(64 << 10)
		if step > start {
			step = start
		}
		start -= step
		chunk := make([]byte, step)
		if _, err := f.ReadAt(chunk, start); err != nil && err != io.EOF {
			return nil, "", 0, err
		}
		data = append(chunk, data...)
	}
	if start > 0 {
		// The window begins mid-line; drop that fragment.
		if i := indexNL(data); i >= 0 {
			data = data[i+1:]
		} else {
			data = nil
		}
	}
	var sp splitter
	var all []string
	collect := func(s string) error { all = append(all, s); return nil }
	if err := sp.feed(data, collect); err != nil {
		return nil, "", 0, err
	}
	if len(sp.pending) > 0 || sp.over {
		partial = sp.take()
	}
	keep := n
	if partial != "" {
		keep-- // the unterminated tail counts as one of the n lines
	}
	if len(all) > keep {
		all = all[len(all)-keep:]
	}
	if partial != "" {
		rawLen = len(data) - (bytes.LastIndexByte(data, '\n') + 1)
	}
	return all, partial, rawLen, nil
}
