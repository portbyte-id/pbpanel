//go:build linux

package fm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// FS performs file operations relative to directory descriptors (development
// plan step 3.2). A path is split into a parent directory and a final name.
// The parent is opened once, and everything after that is done through the
// descriptor with the final component never followed, so a symlink swapped in
// between the check and the use cannot redirect the operation. Recursive walks
// open every directory with O_NOFOLLOW and so never follow a link either.
type FS struct {
	pol Policy

	mu     sync.Mutex
	users  map[uint32]string
	groups map[uint32]string
}

// NewFS returns an FS that judges every path with pol.
func NewFS(pol Policy) *FS {
	return &FS{pol: pol, users: map[uint32]string{}, groups: map[uint32]string{}}
}

const (
	openDir  = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC
	openWalk = openDir | unix.O_NOFOLLOW
)

// parent is an open directory plus the final name inside it.
type parent struct {
	fd   int
	dir  string // the real path of the directory, from /proc/self/fd
	base string // "" when the target is "/"
}

func (p *parent) close() { _ = unix.Close(p.fd) }

// full is the real path of the target: the resolved parent plus the base.
func (p *parent) full() string {
	if p.base == "" {
		return "/"
	}
	return path.Join(p.dir, p.base)
}

// parentOf opens the directory that holds clean. Symlinks in the directory part
// are followed, because the operator navigated there on purpose; the real path
// is read back from the descriptor so the policy judges where the operation
// will actually land, not what the client typed.
func (f *FS) parentOf(clean string) (*parent, error) {
	dir, base := path.Split(clean)
	if clean == "/" {
		dir, base = "/", ""
	}
	fd, err := unix.Open(dir, openDir, 0)
	if err != nil {
		return nil, mapErr(err)
	}
	real, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("resolving %s: %w", dir, err)
	}
	return &parent{fd: fd, dir: real, base: base}, nil
}

// mapErr turns an errno into a package sentinel the API understands.
func mapErr(err error) error {
	switch {
	case errors.Is(err, unix.ENOENT):
		return ErrNotFound
	case errors.Is(err, unix.EEXIST), errors.Is(err, unix.ENOTEMPTY):
		return ErrExists
	case errors.Is(err, unix.ENOTDIR):
		return ErrNotDir
	case errors.Is(err, unix.EISDIR):
		return ErrIsDir
	case errors.Is(err, unix.ELOOP):
		return ErrSymlink
	case errors.Is(err, unix.ENOSPC), errors.Is(err, unix.EDQUOT):
		return ErrNoSpace
	}
	return err
}

func lstatAt(fd int, name string) (unix.Stat_t, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return st, mapErr(err)
	}
	return st, nil
}

func isDir(st unix.Stat_t) bool  { return st.Mode&unix.S_IFMT == unix.S_IFDIR }
func isReg(st unix.Stat_t) bool  { return st.Mode&unix.S_IFMT == unix.S_IFREG }
func isLink(st unix.Stat_t) bool { return st.Mode&unix.S_IFMT == unix.S_IFLNK }

func kindOf(st unix.Stat_t) Kind {
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return KindDir
	case unix.S_IFREG:
		return KindFile
	case unix.S_IFLNK:
		return KindSymlink
	}
	return KindOther
}

func readlinkAt(fd int, name string) (string, error) {
	buf := make([]byte, 4096)
	n, err := unix.Readlinkat(fd, name, buf)
	if err != nil {
		return "", mapErr(err)
	}
	return string(buf[:n]), nil
}

func (f *FS) ownerName(uid uint32) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n, ok := f.users[uid]; ok {
		return n
	}
	name := strconv.FormatUint(uint64(uid), 10)
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	}
	f.users[uid] = name
	return name
}

func (f *FS) groupName(gid uint32) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n, ok := f.groups[gid]; ok {
		return n
	}
	name := strconv.FormatUint(uint64(gid), 10)
	if g, err := user.LookupGroupId(name); err == nil {
		name = g.Name
	}
	f.groups[gid] = name
	return name
}

// entryFrom builds an Entry for name inside the directory open at fd.
func (f *FS) entryFrom(fd int, dir, name string, st unix.Stat_t) Entry {
	e := Entry{
		Name: name, Path: path.Join(dir, name), Kind: kindOf(st),
		Size: st.Size, Mode: st.Mode & 0o7777, UID: st.Uid, GID: st.Gid,
		MTime: st.Mtim.Nano(),
	}
	e.Owner, e.Group = f.ownerName(st.Uid), f.groupName(st.Gid)
	if e.Kind == KindSymlink {
		e.Target, _ = readlinkAt(fd, name)
		var ts unix.Stat_t
		if unix.Fstatat(fd, name, &ts, 0) == nil {
			e.TargetKind = kindOf(ts)
		}
	}
	return e
}

// Stat describes one path without following a final symlink.
func (f *FS) Stat(clean string) (Entry, error) {
	if clean == "/" {
		var st unix.Stat_t
		if err := unix.Stat("/", &st); err != nil {
			return Entry{}, mapErr(err)
		}
		e := f.entryFrom(-1, "/", "", st)
		e.Name, e.Path = "/", "/"
		return e, nil
	}
	p, err := f.parentOf(clean)
	if err != nil {
		return Entry{}, err
	}
	defer p.close()
	st, err := lstatAt(p.fd, p.base)
	if err != nil {
		return Entry{}, err
	}
	return f.entryFrom(p.fd, p.dir, p.base, st), nil
}

// List returns one page of a directory.
func (f *FS) List(clean string, opt ListOptions) (Listing, error) {
	fd, err := unix.Open(clean, openDir, 0)
	if err != nil {
		return Listing{}, mapErr(err)
	}
	real, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil {
		_ = unix.Close(fd)
		return Listing{}, err
	}
	dir := os.NewFile(uintptr(fd), real)
	defer dir.Close()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return Listing{}, mapErr(err)
	}
	entries := make([]Entry, 0, len(names))
	for _, n := range names {
		if !opt.Hidden && strings.HasPrefix(n, ".") {
			continue
		}
		st, err := lstatAt(fd, n)
		if err != nil {
			// Removed between the read and the stat: it is simply gone.
			continue
		}
		entries = append(entries, f.entryFrom(fd, real, n, st))
	}

	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		// Folders first, in every ordering: that is how every file manager reads.
		ad, bd := a.Kind == KindDir || a.TargetKind == KindDir, b.Kind == KindDir || b.TargetKind == KindDir
		if ad != bd {
			return ad
		}
		var less bool
		switch opt.Sort {
		case "size":
			if a.Size != b.Size {
				less = a.Size < b.Size
				break
			}
			less = strings.ToLower(a.Name) < strings.ToLower(b.Name)
		case "mtime":
			if a.MTime != b.MTime {
				less = a.MTime < b.MTime
				break
			}
			less = strings.ToLower(a.Name) < strings.ToLower(b.Name)
		default:
			la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name)
			if la != lb {
				less = la < lb
			} else {
				less = a.Name < b.Name
			}
		}
		if opt.Desc {
			return !less
		}
		return less
	})

	limit := opt.Limit
	switch {
	case limit <= 0:
		limit = DefaultListLimit
	case limit > MaxListLimit:
		limit = MaxListLimit
	}
	total := len(entries)
	off := max(0, min(opt.Offset, total))
	end := min(off+limit, total)
	return Listing{Path: real, Entries: entries[off:end], Total: total, Offset: off}, nil
}

// pseudoFS are filesystems with no storage worth reporting.
var pseudoFS = map[string]bool{
	"proc": true, "sysfs": true, "devpts": true, "cgroup": true, "cgroup2": true,
	"securityfs": true, "debugfs": true, "tracefs": true, "configfs": true,
	"fusectl": true, "bpf": true, "pstore": true, "mqueue": true, "hugetlbfs": true,
	"autofs": true, "binfmt_misc": true, "nsfs": true, "overlay": true,
	"devtmpfs": true, "squashfs": true, "ramfs": true, "rpc_pipefs": true,
}

// Disks reports usage per mounted filesystem.
func (f *FS) Disks() ([]Mount, error) {
	raw, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Mount
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || pseudoFS[fields[2]] {
			continue
		}
		mp := unescapeMount(fields[1])
		if seen[mp] || strings.HasPrefix(mp, "/var/lib/docker/") || strings.HasPrefix(mp, "/run/") {
			continue
		}
		seen[mp] = true
		var sfs unix.Statfs_t
		if err := unix.Statfs(mp, &sfs); err != nil || sfs.Blocks == 0 {
			continue
		}
		bs := uint64(sfs.Bsize)
		out = append(out, Mount{
			Device: fields[0], Path: mp, Type: fields[2],
			Total: sfs.Blocks * bs, Free: sfs.Bavail * bs,
			Used: (sfs.Blocks - sfs.Bfree) * bs,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// unescapeMount decodes the octal escapes /proc/mounts uses for spaces.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// freeBytes is the space available to an unprivileged writer on the
// filesystem holding dir.
func freeBytes(dir string) (uint64, error) {
	var sfs unix.Statfs_t
	if err := unix.Statfs(dir, &sfs); err != nil {
		return 0, err
	}
	return sfs.Bavail * uint64(sfs.Bsize), nil
}

// Mkdir creates a folder. Parents must exist.
func (f *FS) Mkdir(clean string, mode uint32) error {
	return f.create(clean, func(p *parent) error {
		return mapErr(unix.Mkdirat(p.fd, p.base, mode&0o7777))
	})
}

// Touch creates an empty file and fails if the name is taken.
func (f *FS) Touch(clean string, mode uint32) error {
	return f.create(clean, func(p *parent) error {
		fd, err := unix.Openat(p.fd, p.base, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode&0o7777)
		if err != nil {
			return mapErr(err)
		}
		return unix.Close(fd)
	})
}

func (f *FS) create(clean string, fn func(*parent) error) error {
	if clean == "/" {
		return fmt.Errorf("%w: the filesystem root", ErrExists)
	}
	p, err := f.parentOf(clean)
	if err != nil {
		return err
	}
	defer p.close()
	if err := f.pol.CheckWrite(p.full()); err != nil {
		return err
	}
	return fn(p)
}

// renameNoReplace renames without overwriting. Kernels or filesystems without
// RENAME_NOREPLACE fall back to an existence check, which is racy but still
// refuses the ordinary collision.
func renameNoReplace(sfd int, sname string, dfd int, dname string) error {
	err := unix.Renameat2(sfd, sname, dfd, dname, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
		var st unix.Stat_t
		if unix.Fstatat(dfd, dname, &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
			return unix.EEXIST
		}
		return unix.Renameat(sfd, sname, dfd, dname)
	}
	return err
}

// Rename changes the name of an entry inside its directory.
func (f *FS) Rename(clean, newName string) error {
	if err := CheckName(newName); err != nil {
		return err
	}
	p, err := f.parentOf(clean)
	if err != nil {
		return err
	}
	defer p.close()
	if err := f.pol.CheckDestructive(p.full()); err != nil {
		return err
	}
	if err := f.pol.CheckWrite(path.Join(p.dir, newName)); err != nil {
		return err
	}
	return mapErr(renameNoReplace(p.fd, p.base, p.fd, newName))
}

// Move relocates src into the directory dst, keeping its name. Across
// filesystems it copies and then removes, reporting through progress.
func (f *FS) Move(ctx context.Context, src, dst string, progress Progress) error {
	sp, err := f.parentOf(src)
	if err != nil {
		return err
	}
	defer sp.close()
	if err := f.pol.CheckDestructive(sp.full()); err != nil {
		return err
	}
	dp, err := f.parentOf(path.Join(dst, sp.base))
	if err != nil {
		return err
	}
	defer dp.close()
	if err := f.pol.CheckWrite(dp.dir); err != nil {
		return err
	}
	if Within(sp.full(), dp.dir) {
		return ErrIntoItself
	}
	err = renameNoReplace(sp.fd, sp.base, dp.fd, dp.base)
	if !errors.Is(err, unix.EXDEV) {
		return mapErr(err)
	}
	progress.say("moving %s across filesystems: copy, then remove", sp.full())
	if err := f.copyTree(ctx, sp, dp, true, progress); err != nil {
		// A half copy at the destination is worse than none.
		_ = f.removeTree(ctx, dp.fd, dp.base, nil)
		return err
	}
	return f.removeTree(ctx, sp.fd, sp.base, progress)
}

// Copy duplicates src into the directory dst, recursively. Symlinks are
// copied as symlinks, never followed.
func (f *FS) Copy(ctx context.Context, src, dst string, progress Progress) error {
	sp, err := f.parentOf(src)
	if err != nil {
		return err
	}
	defer sp.close()
	if sp.base == "" {
		return fmt.Errorf("%w: cannot copy the filesystem root", ErrProtected)
	}
	dp, err := f.parentOf(path.Join(dst, sp.base))
	if err != nil {
		return err
	}
	defer dp.close()
	if err := f.pol.CheckWrite(dp.dir); err != nil {
		return err
	}
	if Within(sp.full(), dp.dir) {
		return ErrIntoItself
	}
	return f.copyTree(ctx, sp, dp, false, progress)
}

// copyTree copies sp.base to dp.base. preserve keeps owner and times, which a
// move or a trash round trip needs and an ordinary copy does not.
func (f *FS) copyTree(ctx context.Context, sp, dp *parent, preserve bool, progress Progress) error {
	return copyAt(ctx, sp.fd, sp.base, dp.fd, dp.base, preserve, progress, sp.full())
}

func copyAt(ctx context.Context, sfd int, sname string, dfd int, dname string, preserve bool, progress Progress, display string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	st, err := lstatAt(sfd, sname)
	if err != nil {
		return err
	}
	switch {
	case isLink(st):
		target, err := readlinkAt(sfd, sname)
		if err != nil {
			return err
		}
		if err := unix.Symlinkat(target, dfd, dname); err != nil {
			return mapErr(err)
		}
		if preserve {
			_ = unix.Fchownat(dfd, dname, int(st.Uid), int(st.Gid), unix.AT_SYMLINK_NOFOLLOW)
		}
		return nil

	case isDir(st):
		if err := unix.Mkdirat(dfd, dname, 0o700); err != nil {
			return mapErr(err)
		}
		src, err := unix.Openat(sfd, sname, openWalk, 0)
		if err != nil {
			return mapErr(err)
		}
		dst, err := unix.Openat(dfd, dname, openWalk, 0)
		if err != nil {
			_ = unix.Close(src)
			return mapErr(err)
		}
		defer unix.Close(dst)
		dir := os.NewFile(uintptr(src), display)
		defer dir.Close()
		names, err := dir.Readdirnames(-1)
		if err != nil {
			return mapErr(err)
		}
		for _, n := range names {
			if err := copyAt(ctx, src, n, dst, n, preserve, progress, display+"/"+n); err != nil {
				return err
			}
		}
		// Mode last, so a read-only source directory can still be filled.
		if err := unix.Fchmod(dst, st.Mode&0o7777); err != nil {
			return mapErr(err)
		}
		if preserve {
			_ = unix.Fchown(dst, int(st.Uid), int(st.Gid))
			setTimes(dst, st)
		}
		return nil

	case isReg(st):
		in, err := unix.Openat(sfd, sname, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if err != nil {
			return mapErr(err)
		}
		src := os.NewFile(uintptr(in), display)
		defer src.Close()
		out, err := unix.Openat(dfd, dname, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			return mapErr(err)
		}
		dst := os.NewFile(uintptr(out), dname)
		defer dst.Close()
		if _, err := io.Copy(dst, &ctxReader{ctx, src}); err != nil {
			return mapErr(err)
		}
		if err := unix.Fchmod(out, st.Mode&0o7777); err != nil {
			return mapErr(err)
		}
		if preserve {
			_ = unix.Fchown(out, int(st.Uid), int(st.Gid))
			setTimes(out, st)
		}
		return dst.Sync()

	default:
		progress.say("skipping %s: not a regular file, directory or symlink", display)
		return nil
	}
}

func setTimes(fd int, st unix.Stat_t) {
	ts := []unix.Timespec{st.Atim, st.Mtim}
	_ = unix.UtimesNanoAt(fd, "", ts, unix.AT_EMPTY_PATH)
}

// ctxReader makes a long io.Copy stop when its job is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// removeTree deletes name under fd, recursively, never following a link and
// never leaving the filesystem the walk started on.
func (f *FS) removeTree(ctx context.Context, fd int, name string, progress Progress) error {
	st, err := lstatAt(fd, name)
	if err != nil {
		return err
	}
	return removeAt(ctx, fd, name, st, st.Dev, progress)
}

func removeAt(ctx context.Context, fd int, name string, st unix.Stat_t, dev uint64, progress Progress) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !isDir(st) {
		return mapErr(unix.Unlinkat(fd, name, 0))
	}
	if st.Dev != dev {
		return fmt.Errorf("%w: %s", ErrCrossMount, name)
	}
	dfd, err := unix.Openat(fd, name, openWalk, 0)
	if err != nil {
		return mapErr(err)
	}
	dir := os.NewFile(uintptr(dfd), name)
	names, err := dir.Readdirnames(-1)
	if err != nil {
		_ = dir.Close()
		return mapErr(err)
	}
	for _, n := range names {
		cst, err := lstatAt(dfd, n)
		if err != nil {
			_ = dir.Close()
			return err
		}
		if err := removeAt(ctx, dfd, n, cst, dev, progress); err != nil {
			_ = dir.Close()
			return err
		}
	}
	_ = dir.Close()
	return mapErr(unix.Unlinkat(fd, name, unix.AT_REMOVEDIR))
}

// RemoveTree permanently deletes a path. Callers have already passed the
// policy; this is the primitive trash and purge use.
func (f *FS) RemoveTree(ctx context.Context, clean string, progress Progress) error {
	p, err := f.parentOf(clean)
	if err != nil {
		return err
	}
	defer p.close()
	if p.base == "" {
		return fmt.Errorf("%w: the filesystem root", ErrProtected)
	}
	return f.removeTree(ctx, p.fd, p.base, progress)
}

// Chmod changes permission bits. Recursive changes descend without following
// links and need the typed confirmation.
func (f *FS) Chmod(ctx context.Context, clean string, mode uint32, recursive bool, confirm string, progress Progress) (int, error) {
	if mode > 0o7777 {
		return 0, fmt.Errorf("%w: mode out of range", ErrInvalidPath)
	}
	return f.attr(ctx, clean, recursive, confirm, progress, func(fd int, name string, st unix.Stat_t, top bool) error {
		if isLink(st) {
			if top {
				return ErrSymlink
			}
			return nil // a link has no mode of its own on Linux
		}
		return chmodAt(fd, name, mode)
	})
}

// Chown changes owner and group; a negative id leaves that half alone.
func (f *FS) Chown(ctx context.Context, clean string, uid, gid int, recursive bool, confirm string, progress Progress) (int, error) {
	if uid < 0 && gid < 0 {
		return 0, fmt.Errorf("%w: give an owner or a group", ErrInvalidPath)
	}
	return f.attr(ctx, clean, recursive, confirm, progress, func(fd int, name string, st unix.Stat_t, _ bool) error {
		return mapErr(unix.Fchownat(fd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW))
	})
}

// chmodAt changes a mode without following a final symlink swapped in after
// the stat: it opens the inode with O_PATH|O_NOFOLLOW, checks it is not a
// link, and chmods it through /proc/self/fd, which names that exact inode.
func chmodAt(dirfd int, name string, mode uint32) error {
	fd, err := unix.Openat(dirfd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return mapErr(err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return mapErr(err)
	}
	if isLink(st) {
		return ErrSymlink
	}
	return mapErr(unix.Fchmodat(unix.AT_FDCWD, fmt.Sprintf("/proc/self/fd/%d", fd), mode, 0))
}

func (f *FS) attr(ctx context.Context, clean string, recursive bool, confirm string, progress Progress, apply func(fd int, name string, st unix.Stat_t, top bool) error) (int, error) {
	p, err := f.parentOf(clean)
	if err != nil {
		return 0, err
	}
	defer p.close()
	if p.base == "" && recursive {
		return 0, fmt.Errorf("%w: the filesystem root", ErrProtected)
	}
	if err := f.pol.CheckWrite(p.full()); err != nil {
		return 0, err
	}
	if p.base == "" {
		return 0, fmt.Errorf("%w: the filesystem root", ErrProtected)
	}
	st, err := lstatAt(p.fd, p.base)
	if err != nil {
		return 0, err
	}
	if !recursive || !isDir(st) {
		if recursive && isLink(st) {
			return 0, fmt.Errorf("%w: %s", ErrSymlink, p.full())
		}
		return 1, apply(p.fd, p.base, st, true)
	}
	if err := f.pol.CheckDestructive(p.full()); err != nil {
		return 0, err
	}
	if err := f.pol.CheckConfirm(p.full(), confirm); err != nil {
		return 0, err
	}
	n := 0
	err = walkAttr(ctx, p.fd, p.base, st, st.Dev, apply, &n, progress, true)
	return n, err
}

func walkAttr(ctx context.Context, fd int, name string, st unix.Stat_t, dev uint64, apply func(int, string, unix.Stat_t, bool) error, n *int, progress Progress, top bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if isDir(st) && st.Dev != dev {
		return fmt.Errorf("%w: %s", ErrCrossMount, name)
	}
	if err := apply(fd, name, st, top); err != nil {
		return err
	}
	*n++
	if *n%1000 == 0 {
		progress.say("%d entries changed", *n)
	}
	if !isDir(st) {
		return nil
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
	for _, c := range names {
		cst, err := lstatAt(dfd, c)
		if err != nil {
			return err
		}
		if err := walkAttr(ctx, dfd, c, cst, dev, apply, n, progress, false); err != nil {
			return err
		}
	}
	return nil
}

// treeSize sums the bytes of regular files under name, without following links.
func treeSize(ctx context.Context, fd int, name string, st unix.Stat_t) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !isDir(st) {
		if isReg(st) {
			return st.Size, nil
		}
		return 0, nil
	}
	dfd, err := unix.Openat(fd, name, openWalk, 0)
	if err != nil {
		return 0, mapErr(err)
	}
	dir := os.NewFile(uintptr(dfd), name)
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return 0, mapErr(err)
	}
	var total int64
	for _, c := range names {
		cst, err := lstatAt(dfd, c)
		if err != nil {
			continue
		}
		n, err := treeSize(ctx, dfd, c, cst)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// CheckRecursive runs the guardrails of a recursive chmod or chown without
// touching anything, so the API can refuse (and say why) before it creates a
// job. It judges the real path, so a link to /etc is judged as /etc.
func (f *FS) CheckRecursive(clean, confirm string) error {
	p, err := f.parentOf(clean)
	if err != nil {
		return err
	}
	defer p.close()
	if p.base == "" {
		return fmt.Errorf("%w: the filesystem root", ErrProtected)
	}
	if err := f.pol.CheckDestructive(p.full()); err != nil {
		return err
	}
	st, err := lstatAt(p.fd, p.base)
	if err != nil {
		return err
	}
	if isLink(st) {
		return fmt.Errorf("%w: %s", ErrSymlink, p.full())
	}
	if !isDir(st) {
		return nil
	}
	return f.pol.CheckConfirm(p.full(), confirm)
}
