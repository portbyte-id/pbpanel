//go:build linux

package fm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// sniffLen is how much of a file is searched for a NUL byte.
const sniffLen = 8192

func hashOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// OpenFile reads a text file for editing. Binary files and files over
// fm.editor.max_bytes are refused. Any text file may be edited, including the
// panel's own nginx and PHP config trees under /www/panel: editing those here
// bypasses the `nginx -t` check the config editor runs on save.
func (s *Service) OpenFile(ctx context.Context, raw string) (File, error) {
	clean, err := s.pol.Clean(raw)
	if err != nil {
		return File{}, err
	}
	p, err := s.parentOf(clean)
	if err != nil {
		return File{}, err
	}
	defer p.close()
	if p.base == "" {
		return File{}, ErrIsDir
	}
	return s.readFile(ctx, p)
}

func (s *Service) readFile(ctx context.Context, p *parent) (File, error) {
	// O_NONBLOCK so opening a FIFO cannot hang the request.
	fd, err := unix.Openat(p.fd, p.base, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return File{}, mapErr(err)
	}
	f := os.NewFile(uintptr(fd), p.base)
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return File{}, mapErr(err)
	}
	switch {
	case isDir(st):
		return File{}, ErrIsDir
	case !isReg(st):
		return File{}, fmt.Errorf("%w: not a regular file", ErrBinary)
	}
	max := s.limits(ctx).EditorMaxBytes
	if max > 0 && st.Size > max {
		return File{}, fmt.Errorf("%w: %d bytes, the editor opens up to %d", ErrTooLarge, st.Size, max)
	}
	// Read max+1 so a file that grew after the stat is still caught.
	var r io.Reader = f
	if max > 0 {
		r = io.LimitReader(f, max+1)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return File{}, mapErr(err)
	}
	if max > 0 && int64(len(data)) > max {
		return File{}, fmt.Errorf("%w: the editor opens up to %d bytes", ErrTooLarge, max)
	}
	head := data
	if len(head) > sniffLen {
		head = head[:sniffLen]
	}
	if bytes.IndexByte(head, 0) >= 0 || !utf8.Valid(data) {
		return File{}, ErrBinary
	}
	return File{
		Path: p.full(), Content: string(data), Size: int64(len(data)),
		MTime: st.Mtim.Nano(), Hash: hashOf(data), Mode: st.Mode & 0o7777,
	}, nil
}

// SaveFile writes content atomically. If base is non-nil and the file on disk
// no longer matches it, nothing is written and a *ConflictError carries what is
// there now. The write goes to a temporary file in the same directory, is
// synced, takes the old file's mode and owner, and is renamed into place.
//
// The check and the rename are not one atomic step; a writer that lands
// between them wins silently. The window is the length of one fsync.
func (s *Service) SaveFile(ctx context.Context, raw, content string, base *Base) (File, error) {
	clean, err := s.pol.Clean(raw)
	if err != nil {
		return File{}, err
	}
	p, err := s.parentOf(clean)
	if err != nil {
		return File{}, err
	}
	defer p.close()
	if p.base == "" {
		return File{}, ErrIsDir
	}
	real := p.full()
	if err := s.pol.CheckWrite(real); err != nil {
		return File{}, err
	}
	if s.internalTree(real) {
		return File{}, fmt.Errorf("%w: %s is managed by the panel", ErrProtected, real)
	}
	if max := s.limits(ctx).EditorMaxBytes; max > 0 && int64(len(content)) > max {
		return File{}, fmt.Errorf("%w: %d bytes, the editor saves up to %d", ErrTooLarge, len(content), max)
	}
	if !utf8.ValidString(content) || bytes.IndexByte([]byte(content), 0) >= 0 {
		return File{}, ErrBinary
	}

	mode, uid, gid := uint32(0o644), -1, -1
	cur, err := s.readFile(ctx, p)
	switch {
	case err == nil:
		// The content hash decides. The mtime is not compared: it is a nanosecond
		// int64, and the browser holds it as a float64 that rounds it, so an exact
		// comparison would conflict on every save.
		if base != nil && (cur.Hash != base.Hash || cur.Size != base.Size) {
			return File{}, &ConflictError{Current: cur}
		}
		mode = cur.Mode
		if st, err := lstatAt(p.fd, p.base); err == nil {
			uid, gid = int(st.Uid), int(st.Gid)
			if isLink(st) {
				return File{}, fmt.Errorf("%w: save the link's target instead", ErrSymlink)
			}
		}
	case errors.Is(err, ErrNotFound):
		if base != nil {
			return File{}, fmt.Errorf("%w: the file was deleted since it was opened", ErrNotFound)
		}
	case errors.Is(err, ErrBinary), errors.Is(err, ErrTooLarge):
		// An existing file the editor would refuse to open: the operator
		// is replacing it knowingly only if they sent no base.
		if base != nil {
			return File{}, err
		}
		if st, err := lstatAt(p.fd, p.base); err == nil {
			mode, uid, gid = st.Mode&0o7777, int(st.Uid), int(st.Gid)
		}
	default:
		return File{}, err
	}

	tmp := fmt.Sprintf(".%s.%s.tmp", p.base, newID()[:8])
	if len(tmp) > 255 {
		tmp = fmt.Sprintf(".%s.tmp", newID())
	}
	fd, err := unix.Openat(p.fd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return File{}, mapErr(err)
	}
	f := os.NewFile(uintptr(fd), tmp)
	cleanup := func() { _ = f.Close(); _ = unix.Unlinkat(p.fd, tmp, 0) }
	if _, err := f.WriteString(content); err != nil {
		cleanup()
		return File{}, mapErr(err)
	}
	if err := unix.Fchmod(fd, mode); err != nil {
		cleanup()
		return File{}, mapErr(err)
	}
	if uid >= 0 {
		_ = unix.Fchown(fd, uid, gid)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return File{}, mapErr(err)
	}
	if err := f.Close(); err != nil {
		_ = unix.Unlinkat(p.fd, tmp, 0)
		return File{}, mapErr(err)
	}
	if err := unix.Renameat(p.fd, tmp, p.fd, p.base); err != nil {
		_ = unix.Unlinkat(p.fd, tmp, 0)
		return File{}, mapErr(err)
	}
	_ = unix.Fsync(p.fd)
	return s.readFile(ctx, p)
}
