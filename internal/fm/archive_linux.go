//go:build linux

package fm

import (
	"archive/tar"
	"archive/zip"
	"compress/flate"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Archive formats.
const (
	FormatZip   = "zip"
	FormatTarGz = "tar.gz"
	FormatTar   = "tar"
)

// archiveSink is what the tree walk writes into; tar.gz and zip each have one.
type archiveSink interface {
	dir(name string, st unix.Stat_t) error
	file(name string, st unix.Stat_t, r io.Reader) error
	symlink(name string, st unix.Stat_t, target string) error
	close() error
}

type tarSink struct {
	tw *tar.Writer
	gz *gzip.Writer
}

func newTarSink(w io.Writer, gz bool) *tarSink {
	s := &tarSink{}
	if gz {
		s.gz = gzip.NewWriter(w)
		w = s.gz
	}
	s.tw = tar.NewWriter(w)
	return s
}

func tarHeader(name string, st unix.Stat_t, typ byte, link string) *tar.Header {
	return &tar.Header{
		Name: name, Typeflag: typ, Linkname: link, Mode: int64(st.Mode & 0o7777),
		Uid: int(st.Uid), Gid: int(st.Gid), ModTime: time.Unix(st.Mtim.Unix()),
	}
}

func (s *tarSink) dir(name string, st unix.Stat_t) error {
	return s.tw.WriteHeader(tarHeader(name+"/", st, tar.TypeDir, ""))
}

func (s *tarSink) symlink(name string, st unix.Stat_t, target string) error {
	return s.tw.WriteHeader(tarHeader(name, st, tar.TypeSymlink, target))
}

func (s *tarSink) file(name string, st unix.Stat_t, r io.Reader) error {
	h := tarHeader(name, st, tar.TypeReg, "")
	h.Size = st.Size
	if err := s.tw.WriteHeader(h); err != nil {
		return err
	}
	// A file that grew while it was read would corrupt the stream; copy
	// exactly the announced size, and fail if the file got shorter.
	n, err := io.CopyN(s.tw, r, st.Size)
	if err != nil {
		return fmt.Errorf("%s: read %d of %d bytes: %w", name, n, st.Size, err)
	}
	return nil
}

func (s *tarSink) close() error {
	if err := s.tw.Close(); err != nil {
		return err
	}
	if s.gz != nil {
		return s.gz.Close()
	}
	return nil
}

type zipSink struct{ zw *zip.Writer }

func newZipSink(w io.Writer) *zipSink {
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.DefaultCompression)
	})
	return &zipSink{zw}
}

func zipHeader(name string, st unix.Stat_t, method uint16) *zip.FileHeader {
	h := &zip.FileHeader{Name: name, Method: method, Modified: time.Unix(st.Mtim.Unix())}
	h.SetMode(osMode(st))
	return h
}

func osMode(st unix.Stat_t) os.FileMode {
	m := os.FileMode(st.Mode & 0o777)
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		m |= os.ModeDir
	case unix.S_IFLNK:
		m |= os.ModeSymlink
	}
	return m
}

func (s *zipSink) dir(name string, st unix.Stat_t) error {
	_, err := s.zw.CreateHeader(zipHeader(name+"/", st, zip.Store))
	return err
}

func (s *zipSink) symlink(name string, st unix.Stat_t, target string) error {
	w, err := s.zw.CreateHeader(zipHeader(name, st, zip.Store))
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, target)
	return err
}

func (s *zipSink) file(name string, st unix.Stat_t, r io.Reader) error {
	w, err := s.zw.CreateHeader(zipHeader(name, st, zip.Deflate))
	if err != nil {
		return err
	}
	_, err = io.CopyN(w, r, st.Size)
	return err
}

func (s *zipSink) close() error { return s.zw.Close() }

func newSink(format string, w io.Writer) (archiveSink, error) {
	switch format {
	case FormatZip:
		return newZipSink(w), nil
	case FormatTarGz:
		return newTarSink(w, true), nil
	case FormatTar:
		return newTarSink(w, false), nil
	}
	return nil, fmt.Errorf("%w: unknown format %q (use zip, tar.gz or tar)", ErrInvalidPath, format)
}

// addTree writes name (open under fd) and everything below it. It never follows
// a symlink: links are stored as links, directories are opened O_NOFOLLOW.
// skipIno is the inode of the archive being written, which must not contain
// itself.
func addTree(ctx context.Context, sink archiveSink, fd int, name, arcName string, st unix.Stat_t, skip *unix.Stat_t, progress Progress, count *int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if skip != nil && st.Dev == skip.Dev && st.Ino == skip.Ino {
		return nil
	}
	*count++
	if *count%500 == 0 {
		progress.say("%d entries archived", *count)
	}
	switch {
	case isLink(st):
		target, err := readlinkAt(fd, name)
		if err != nil {
			return err
		}
		return sink.symlink(arcName, st, target)
	case isDir(st):
		if err := sink.dir(arcName, st); err != nil {
			return err
		}
		dfd, err := unix.Openat(fd, name, openWalk, 0)
		if err != nil {
			return mapErr(err)
		}
		dir := os.NewFile(uintptr(dfd), name)
		defer dir.Close()
		names, err := dir.Readdirnames(-1)
		if err != nil {
			return mapErr(err)
		}
		for _, n := range names {
			cst, err := lstatAt(dfd, n)
			if err != nil {
				continue
			}
			if err := addTree(ctx, sink, dfd, n, arcName+"/"+n, cst, skip, progress, count); err != nil {
				return err
			}
		}
		return nil
	case isReg(st):
		in, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if err != nil {
			return mapErr(err)
		}
		f := os.NewFile(uintptr(in), name)
		defer f.Close()
		return sink.file(arcName, st, &ctxReader{ctx, f})
	}
	return nil // sockets, devices, FIFOs are not archived
}

// writeArchive archives names (entries of the directory open at pfd).
func writeArchive(ctx context.Context, w io.Writer, format string, pfd int, names []string, skip *unix.Stat_t, progress Progress) error {
	sink, err := newSink(format, w)
	if err != nil {
		return err
	}
	count := 0
	for _, n := range names {
		st, err := lstatAt(pfd, n)
		if err != nil {
			return err
		}
		if err := addTree(ctx, sink, pfd, n, n, st, skip, progress, &count); err != nil {
			return err
		}
	}
	return sink.close()
}

// StreamFolder writes a folder (or a single path) to w as an archive, with no
// temporary file. It is the download of a folder.
func (s *Service) StreamFolder(ctx context.Context, w io.Writer, raw, format string) error {
	clean, err := s.pol.Clean(raw)
	if err != nil {
		return err
	}
	p, err := s.parentOf(clean)
	if err != nil {
		return err
	}
	defer p.close()
	if p.base == "" {
		return fmt.Errorf("%w: archive a folder inside the filesystem root instead", ErrProtected)
	}
	if _, err := lstatAt(p.fd, p.base); err != nil {
		return err
	}
	return writeArchive(ctx, w, format, p.fd, []string{p.base}, nil, nil)
}

// CreateArchive archives names, all entries of the directory dir, into the
// file dest (a full path whose directory must exist).
func (s *Service) CreateArchive(ctx context.Context, dir string, names []string, dest, format string, progress Progress) error {
	cdir, err := s.pol.Clean(dir)
	if err != nil {
		return err
	}
	cdest, err := s.pol.Clean(dest)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("%w: nothing selected", ErrInvalidPath)
	}
	for _, n := range names {
		if err := CheckName(n); err != nil {
			return err
		}
	}
	sp, err := s.parentOf(path.Join(cdir, "x"))
	if err != nil {
		return err
	}
	defer sp.close()
	dp, err := s.parentOf(cdest)
	if err != nil {
		return err
	}
	defer dp.close()
	if dp.base == "" {
		return ErrIsDir
	}
	if err := s.pol.CheckWrite(dp.full()); err != nil {
		return err
	}
	if s.internalTree(dp.full()) {
		return fmt.Errorf("%w: %s is managed by the panel", ErrProtected, dp.full())
	}

	fd, err := unix.Openat(dp.fd, dp.base, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return mapErr(err)
	}
	out := os.NewFile(uintptr(fd), dp.base)
	var self unix.Stat_t
	_ = unix.Fstat(fd, &self)

	err = writeArchive(ctx, out, format, sp.fd, names, &self, progress)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = unix.Unlinkat(dp.fd, dp.base, 0)
		return err
	}
	return nil
}

// --- extraction -----------------------------------------------------------

type archiveKind int

const (
	entryFile archiveKind = iota
	entryDir
	entrySymlink
	entryHardlink
)

type archiveEntry struct {
	name   string // cleaned, relative, never starts with ../
	kind   archiveKind
	mode   uint32 // permission bits only
	size   int64
	target string // symlink or hardlink target
	mtime  time.Time
}

// ArchiveFormatOf picks a format from a file name, or "".
func ArchiveFormatOf(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.HasSuffix(l, ".zip"):
		return FormatZip
	case strings.HasSuffix(l, ".tar.gz"), strings.HasSuffix(l, ".tgz"):
		return FormatTarGz
	case strings.HasSuffix(l, ".tar"):
		return FormatTar
	}
	return ""
}

func archiveErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrArchive, fmt.Sprintf(format, args...))
}

// entryName validates and cleans one archive path. It rejects anything that
// could name a place outside the extraction directory.
func entryName(raw string) (string, error) {
	switch {
	case raw == "":
		return "", archiveErr("an entry has an empty name")
	case strings.ContainsRune(raw, 0):
		return "", archiveErr("entry name contains a NUL byte")
	case strings.HasPrefix(raw, "/"):
		return "", archiveErr("%q is an absolute path", raw)
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return "", archiveErr("%q climbs out of the target with ..", raw)
		}
	}
	clean := path.Clean(raw)
	if clean == "." {
		return "", nil // the archive's own root directory entry
	}
	if len(clean) > 4096 {
		return "", archiveErr("entry name is too long")
	}
	return clean, nil
}

// linkTarget validates a symlink target: relative, and lexically staying inside
// the extraction directory when resolved from where the link sits.
func linkTarget(name, target string) error {
	switch {
	case target == "":
		return archiveErr("%s: symlink with an empty target", name)
	case strings.ContainsRune(target, 0):
		return archiveErr("%s: symlink target contains a NUL byte", name)
	case strings.HasPrefix(target, "/"):
		return archiveErr("%s: symlink to the absolute path %q", name, target)
	}
	joined := path.Join(path.Dir(name), target)
	if joined == ".." || strings.HasPrefix(joined, "../") {
		return archiveErr("%s: symlink to %q points outside the target", name, target)
	}
	return nil
}

// archiveReader iterates an archive. fn gets each entry and a reader for its
// bytes (valid only during the call).
type archiveReader func(fn func(archiveEntry, io.Reader) error) error

func openArchive(f *os.File, format string) (archiveReader, func(), error) {
	switch format {
	case FormatZip:
		st, err := f.Stat()
		if err != nil {
			return nil, nil, err
		}
		zr, err := zip.NewReader(f, st.Size())
		if err != nil {
			return nil, nil, archiveErr("not a readable zip file: %v", err)
		}
		return func(fn func(archiveEntry, io.Reader) error) error {
			for _, zf := range zr.File {
				e := archiveEntry{name: zf.Name, mode: uint32(zf.Mode().Perm()), size: int64(zf.UncompressedSize64), mtime: zf.Modified}
				m := zf.Mode()
				var r io.Reader = eofReader{}
				switch {
				case m&os.ModeDir != 0 || strings.HasSuffix(zf.Name, "/"):
					e.kind = entryDir
				case m&os.ModeSymlink != 0:
					e.kind = entrySymlink
					rc, err := zf.Open()
					if err != nil {
						return archiveErr("%s: %v", zf.Name, err)
					}
					b, err := io.ReadAll(io.LimitReader(rc, 4097))
					_ = rc.Close()
					if err != nil || len(b) > 4096 {
						return archiveErr("%s: unreadable symlink target", zf.Name)
					}
					e.target = string(b)
				case m.IsRegular():
					e.kind = entryFile
					rc, err := zf.Open()
					if err != nil {
						return archiveErr("%s: %v", zf.Name, err)
					}
					if err := fn(e, rc); err != nil {
						_ = rc.Close()
						return err
					}
					_ = rc.Close()
					continue
				default:
					return archiveErr("%s: not a file, folder or symlink (device or pipe)", zf.Name)
				}
				if err := fn(e, r); err != nil {
					return err
				}
			}
			return nil
		}, func() {}, nil

	case FormatTarGz, FormatTar:
		var src io.Reader = f
		var gz *gzip.Reader
		if format == FormatTarGz {
			var err error
			if gz, err = gzip.NewReader(f); err != nil {
				return nil, nil, archiveErr("not a readable gzip file: %v", err)
			}
			src = gz
		}
		tr := tar.NewReader(src)
		return func(fn func(archiveEntry, io.Reader) error) error {
				for {
					h, err := tr.Next()
					if err == io.EOF {
						return nil
					}
					if err != nil {
						return archiveErr("%v", err)
					}
					e := archiveEntry{name: h.Name, mode: uint32(h.Mode) & 0o777, size: h.Size, target: h.Linkname, mtime: h.ModTime}
					switch h.Typeflag {
					case tar.TypeReg:
						e.kind = entryFile
					case tar.TypeDir:
						e.kind = entryDir
					case tar.TypeSymlink:
						e.kind = entrySymlink
					case tar.TypeLink:
						e.kind = entryHardlink
					case tar.TypeXGlobalHeader:
						continue
					default:
						return archiveErr("%s: not a file, folder or link (device or pipe)", h.Name)
					}
					if err := fn(e, tr); err != nil {
						return err
					}
				}
			}, func() {
				if gz != nil {
					_ = gz.Close()
				}
			}, nil
	}
	return nil, nil, fmt.Errorf("%w: unknown archive format", ErrInvalidPath)
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

// extractRun is one pass over an archive. With write false it only validates
// (and counts real decompressed bytes, so a header that lies about a size does
// not get past it); with write true it creates files.
type extractRun struct {
	ctx       context.Context
	root      int // descriptor of the target directory
	lim       Limits
	write     bool
	overwrite bool
	progress  Progress

	entries int
	bytes   int64
	files   map[string]bool // regular files seen, for hardlinks
	links   map[string]bool // symlinks seen, which later entries must not pass through
}

func (x *extractRun) handle(e archiveEntry, r io.Reader) error {
	if err := x.ctx.Err(); err != nil {
		return err
	}
	name, err := entryName(e.name)
	if err != nil {
		return err
	}
	if name == "" {
		return nil
	}
	x.entries++
	if x.lim.ArchiveMaxEntries > 0 && x.entries > x.lim.ArchiveMaxEntries {
		return archiveErr("more than %d entries", x.lim.ArchiveMaxEntries)
	}
	// An entry may not be written through a symlink the archive itself made.
	for p := path.Dir(name); p != "."; p = path.Dir(p) {
		if x.links[p] {
			return archiveErr("%s: passes through the symlink %s", name, p)
		}
	}
	comps := strings.Split(name, "/")
	perm := e.mode & 0o777

	switch e.kind {
	case entryDir:
		if !x.write {
			return nil
		}
		fd, err := mkdirAll(x.root, comps, 0o755)
		if err != nil {
			return err
		}
		_ = unix.Fchmod(fd, perm|0o700)
		return unix.Close(fd)

	case entrySymlink:
		if err := linkTarget(name, e.target); err != nil {
			return err
		}
		x.links[name] = true
		if !x.write {
			return nil
		}
		pfd, err := mkdirAll(x.root, comps[:len(comps)-1], 0o755)
		if err != nil {
			return err
		}
		defer unix.Close(pfd)
		last := comps[len(comps)-1]
		if x.overwrite {
			_ = unix.Unlinkat(pfd, last, 0)
		}
		return mapErr(unix.Symlinkat(e.target, pfd, last))

	case entryHardlink:
		target, err := entryName(e.target)
		if err != nil || target == "" || !x.files[target] {
			return archiveErr("%s: hard link to %q, which is not a file earlier in the archive", name, e.target)
		}
		x.files[name] = true
		if !x.write {
			return nil
		}
		pfd, err := mkdirAll(x.root, comps[:len(comps)-1], 0o755)
		if err != nil {
			return err
		}
		defer unix.Close(pfd)
		tcomps := strings.Split(target, "/")
		tfd, err := mkdirAll(x.root, tcomps[:len(tcomps)-1], 0o755)
		if err != nil {
			return err
		}
		defer unix.Close(tfd)
		last := comps[len(comps)-1]
		if x.overwrite {
			_ = unix.Unlinkat(pfd, last, 0)
		}
		return mapErr(unix.Linkat(tfd, tcomps[len(tcomps)-1], pfd, last, 0))
	}

	// Regular file.
	x.files[name] = true
	if !x.write {
		if !x.overwrite {
			if st, err := statComps(x.root, comps); err == nil && !isDir(st) {
				return fmt.Errorf("%w: %s already exists", ErrExists, name)
			}
		}
		return x.count(io.Discard, r, name, e.size)
	}
	pfd, err := mkdirAll(x.root, comps[:len(comps)-1], 0o755)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	last := comps[len(comps)-1]
	if x.overwrite {
		_ = unix.Unlinkat(pfd, last, 0) // never write through an existing link
	}
	fd, err := unix.Openat(pfd, last, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, perm|0o600)
	if err != nil {
		return mapErr(err)
	}
	out := os.NewFile(uintptr(fd), name)
	if err := x.count(out, r, name, e.size); err != nil {
		_ = out.Close()
		return err
	}
	if err := unix.Fchmod(fd, perm); err != nil { // no setuid, setgid or sticky
		_ = out.Close()
		return mapErr(err)
	}
	if !e.mtime.IsZero() {
		ts := []unix.Timespec{unix.NsecToTimespec(e.mtime.UnixNano()), unix.NsecToTimespec(e.mtime.UnixNano())}
		_ = unix.UtimesNanoAt(fd, "", ts, unix.AT_EMPTY_PATH)
	}
	return out.Close()
}

// count copies r to w, enforcing the total size limit on the bytes actually
// produced, not on what the header claims.
func (x *extractRun) count(w io.Writer, r io.Reader, name string, claimed int64) error {
	remaining := int64(-1)
	if x.lim.ArchiveMaxBytes > 0 {
		remaining = x.lim.ArchiveMaxBytes - x.bytes
		if claimed > remaining {
			return archiveErr("%s: would exceed the %d byte extraction limit", name, x.lim.ArchiveMaxBytes)
		}
	}
	src := &ctxReader{x.ctx, r}
	var n int64
	var err error
	if remaining >= 0 {
		n, err = io.Copy(w, io.LimitReader(src, remaining+1))
		x.bytes += n
		if err == nil && n > remaining {
			return archiveErr("%s: expands past the %d byte extraction limit", name, x.lim.ArchiveMaxBytes)
		}
	} else {
		n, err = io.Copy(w, src)
		x.bytes += n
	}
	if err != nil {
		return mapErr(err)
	}
	return nil
}

// mkdirAll walks comps below root, creating missing folders, opening every
// component O_NOFOLLOW so a symlink in the way is an error and never a detour.
// It returns a descriptor for the last component (root itself for none).
func mkdirAll(root int, comps []string, mode uint32) (int, error) {
	cur, err := unix.Dup(root)
	if err != nil {
		return -1, err
	}
	for _, c := range comps {
		next, err := unix.Openat(cur, c, openWalk, 0)
		if errors.Is(err, unix.ENOENT) {
			if err = unix.Mkdirat(cur, c, mode); err == nil {
				next, err = unix.Openat(cur, c, openWalk, 0)
			}
		}
		_ = unix.Close(cur)
		if err != nil {
			if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
				return -1, archiveErr("%s: a path component is a symlink or a file", strings.Join(comps, "/"))
			}
			return -1, mapErr(err)
		}
		cur = next
	}
	return cur, nil
}

func statComps(root int, comps []string) (unix.Stat_t, error) {
	pfd, err := unix.Dup(root)
	if err != nil {
		return unix.Stat_t{}, err
	}
	for _, c := range comps[:len(comps)-1] {
		next, err := unix.Openat(pfd, c, openWalk, 0)
		_ = unix.Close(pfd)
		if err != nil {
			return unix.Stat_t{}, err
		}
		pfd = next
	}
	defer unix.Close(pfd)
	var st unix.Stat_t
	err = unix.Fstatat(pfd, comps[len(comps)-1], &st, unix.AT_SYMLINK_NOFOLLOW)
	return st, err
}

// Extract unpacks archive into the directory dest. It reads the archive twice:
// a first pass writes nothing and rejects any entry that is unsafe, over the
// limits or colliding, a second pass extracts. A hostile archive is therefore
// refused before it has written a byte. Every write goes through descriptors
// opened O_NOFOLLOW, so it cannot leave dest even if the first pass missed a
// case.
func (s *Service) Extract(ctx context.Context, archive, dest string, overwrite bool, progress Progress) error {
	carch, err := s.pol.Clean(archive)
	if err != nil {
		return err
	}
	format := ArchiveFormatOf(carch)
	if format == "" {
		return fmt.Errorf("%w: not a .zip, .tar.gz, .tgz or .tar file", ErrInvalidPath)
	}
	cdest, err := s.pol.Clean(dest)
	if err != nil {
		return err
	}
	rfd, err := unix.Open(cdest, openDir, 0)
	if err != nil {
		return mapErr(err)
	}
	defer unix.Close(rfd)
	real, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", rfd))
	if err != nil {
		return err
	}
	if err := s.pol.CheckWrite(real); err != nil {
		return err
	}
	if s.internalTree(real) {
		return fmt.Errorf("%w: %s is managed by the panel", ErrProtected, real)
	}
	lim := s.limits(ctx)

	for _, write := range []bool{false, true} {
		f, err := os.Open(carch)
		if err != nil {
			return mapErr(err)
		}
		read, closeFn, err := openArchive(f, format)
		if err != nil {
			_ = f.Close()
			return err
		}
		run := &extractRun{
			ctx: ctx, root: rfd, lim: lim, write: write, overwrite: overwrite,
			progress: progress, files: map[string]bool{}, links: map[string]bool{},
		}
		if write {
			progress.say("extracting into %s", real)
		} else {
			progress.say("checking the archive")
		}
		err = read(run.handle)
		closeFn()
		_ = f.Close()
		if err != nil {
			return err
		}
		if write {
			progress.say("extracted %d entries, %d bytes", run.entries, run.bytes)
		}
	}
	return nil
}
