//go:build linux

package fm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Chunked upload (development plan step 3.7). The client announces target and
// size, sends chunks at the offset the server reports with a SHA-256 each, and
// completes. Bytes accumulate in <panel>/uploads/<id>.part; the target only
// ever sees the finished file, moved in with one rename.

func (s *Service) partPath(id string) string { return path.Join(s.uploadDir, id+".part") }

func (s *Service) lockFor(id string) *sync.Mutex {
	m, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// UploadStart records an upload. Its size is checked against the free space of
// the uploads directory; beyond that only the disk limits it.
func (s *Service) UploadStart(ctx context.Context, dir, name string, size int64, overwrite bool) (UploadInfo, error) {
	if size < 0 {
		return UploadInfo{}, fmt.Errorf("%w: negative size", ErrInvalidPath)
	}
	if err := CheckName(name); err != nil {
		return UploadInfo{}, err
	}
	cdir, err := s.pol.Clean(dir)
	if err != nil {
		return UploadInfo{}, err
	}
	dp, err := s.parentOf(path.Join(cdir, name))
	if err != nil {
		return UploadInfo{}, err
	}
	defer dp.close()
	if err := s.pol.CheckWrite(dp.dir); err != nil {
		return UploadInfo{}, err
	}
	if s.internalTree(dp.dir) {
		return UploadInfo{}, fmt.Errorf("%w: %s is managed by the panel", ErrProtected, dp.dir)
	}
	if st, err := lstatAt(dp.fd, dp.base); err == nil {
		if isDir(st) {
			return UploadInfo{}, fmt.Errorf("%w: %s is a folder", ErrIsDir, dp.full())
		}
		if !overwrite {
			return UploadInfo{}, fmt.Errorf("%w: %s", ErrExists, dp.full())
		}
	}
	free, err := freeBytes(s.uploadDir)
	if err != nil {
		return UploadInfo{}, err
	}
	if uint64(size) > free {
		return UploadInfo{}, fmt.Errorf("%w: %d bytes needed, %d free", ErrNoSpace, size, free)
	}

	id := newID()
	fd, err := unix.Open(s.partPath(id), unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return UploadInfo{}, mapErr(err)
	}
	_ = unix.Close(fd)
	now := s.now().UTC().Format(timeLayout)
	if _, err := s.st.DB().ExecContext(ctx,
		`INSERT INTO fm_uploads (id, dir, name, size, received, overwrite, created_at, updated_at) VALUES (?,?,?,?,0,?,?,?)`,
		id, dp.dir, name, size, boolInt(overwrite), now, now); err != nil {
		_ = os.Remove(s.partPath(id))
		return UploadInfo{}, err
	}
	return UploadInfo{ID: id, Dir: dp.dir, Name: name, Size: size, ChunkMax: s.limits(ctx).ChunkMaxBytes, Overwrite: overwrite}, nil
}

func (s *Service) uploadRow(ctx context.Context, id string) (UploadInfo, error) {
	var (
		u  UploadInfo
		ow int
	)
	if !validID(id) {
		return u, ErrNotFound
	}
	err := s.st.DB().QueryRowContext(ctx,
		`SELECT id, dir, name, size, received, overwrite FROM fm_uploads WHERE id = ?`, id).
		Scan(&u.ID, &u.Dir, &u.Name, &u.Size, &u.Received, &ow)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.Overwrite = ow != 0
	u.ChunkMax = s.limits(ctx).ChunkMaxBytes
	return u, err
}

// UploadStatus reports how many bytes the server holds, which is where a
// resuming client continues.
func (s *Service) UploadStatus(ctx context.Context, id string) (UploadInfo, error) {
	return s.uploadRow(ctx, id)
}

// UploadChunk appends one chunk. offset must equal the bytes received so far;
// sum is the hex SHA-256 of the chunk and is required. A chunk that fails any
// check changes nothing.
func (s *Service) UploadChunk(ctx context.Context, id string, offset int64, sum string, body io.Reader) (UploadInfo, error) {
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	u, err := s.uploadRow(ctx, id)
	if err != nil {
		return u, err
	}
	if offset != u.Received {
		return u, &OffsetError{Received: u.Received}
	}
	want, err := hex.DecodeString(sum)
	if err != nil || len(want) != sha256.Size {
		return u, fmt.Errorf("%w: a SHA-256 of the chunk is required", ErrChecksum)
	}
	max := u.ChunkMax
	if max <= 0 {
		max = 8 << 20
	}
	buf, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return u, err
	}
	if int64(len(buf)) > max {
		return u, fmt.Errorf("%w: a chunk is at most %d bytes", ErrTooLarge, max)
	}
	if len(buf) == 0 {
		return u, fmt.Errorf("%w: empty chunk", ErrInvalidPath)
	}
	if u.Received+int64(len(buf)) > u.Size {
		return u, fmt.Errorf("%w: the chunk runs past the announced size", ErrTooLarge)
	}
	got := sha256.Sum256(buf)
	if hex.EncodeToString(got[:]) != strings.ToLower(sum) {
		return u, ErrChecksum
	}

	f, err := os.OpenFile(s.partPath(id), os.O_WRONLY, 0)
	if err != nil {
		return u, mapErr(err)
	}
	defer f.Close()
	if _, err := f.WriteAt(buf, offset); err != nil {
		return u, mapErr(err)
	}
	if err := f.Sync(); err != nil {
		return u, mapErr(err)
	}
	u.Received += int64(len(buf))
	if _, err := s.st.DB().ExecContext(ctx,
		`UPDATE fm_uploads SET received = ?, updated_at = ? WHERE id = ?`,
		u.Received, s.now().UTC().Format(timeLayout), id); err != nil {
		return u, err
	}
	return u, nil
}

// UploadComplete verifies and moves the finished file into place, returning its
// path. wholeSum is optional; when given it must match the assembled bytes.
func (s *Service) UploadComplete(ctx context.Context, id, wholeSum string) (string, error) {
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	u, err := s.uploadRow(ctx, id)
	if err != nil {
		return "", err
	}
	if u.Received != u.Size {
		return "", &OffsetError{Received: u.Received}
	}
	part := s.partPath(id)
	if wholeSum != "" {
		f, err := os.Open(part)
		if err != nil {
			return "", mapErr(err)
		}
		h := sha256.New()
		_, err = io.Copy(h, &ctxReader{ctx, f})
		_ = f.Close()
		if err != nil {
			return "", err
		}
		if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(wholeSum) {
			return "", ErrChecksum
		}
	}

	// The target directory is judged again now: it may have changed since init.
	dp, err := s.parentOf(path.Join(u.Dir, u.Name))
	if err != nil {
		return "", err
	}
	defer dp.close()
	if err := s.pol.CheckWrite(dp.dir); err != nil {
		return "", err
	}
	mode, uid, gid := uint32(0o644), -1, -1
	if st, err := lstatAt(dp.fd, dp.base); err == nil {
		switch {
		case isDir(st):
			return "", fmt.Errorf("%w: %s is a folder", ErrIsDir, dp.full())
		case !u.Overwrite:
			return "", fmt.Errorf("%w: %s", ErrExists, dp.full())
		}
		mode, uid, gid = st.Mode&0o7777, int(st.Uid), int(st.Gid)
	}

	if err := s.placePart(ctx, part, dp, mode, uid, gid, u.Overwrite); err != nil {
		return "", err
	}
	_, _ = s.st.DB().ExecContext(context.WithoutCancel(ctx), `DELETE FROM fm_uploads WHERE id = ?`, id)
	s.locks.Delete(id)
	return dp.full(), nil
}

// placePart moves the assembled file to dp. Same filesystem: one rename, which
// is atomic. Different filesystem: copy beside the target, sync, rename.
func (s *Service) placePart(ctx context.Context, part string, dp *parent, mode uint32, uid, gid int, overwrite bool) error {
	pp, err := s.parentOf(part)
	if err != nil {
		return err
	}
	defer pp.close()
	if err := unix.Fchmodat(unix.AT_FDCWD, part, mode, 0); err != nil {
		return mapErr(err)
	}
	if uid >= 0 {
		_ = unix.Fchownat(pp.fd, pp.base, uid, gid, unix.AT_SYMLINK_NOFOLLOW)
	}
	rename := func(sfd int, sname string, dname string) error {
		if overwrite {
			return unix.Renameat(sfd, sname, dp.fd, dname)
		}
		return renameNoReplace(sfd, sname, dp.fd, dname)
	}
	err = rename(pp.fd, pp.base, dp.base)
	if !errors.Is(err, unix.EXDEV) {
		return mapErr(err)
	}

	tmp := fmt.Sprintf(".%s.%s.upload", dp.base, newID()[:8])
	out, err := unix.Openat(dp.fd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return mapErr(err)
	}
	dst := os.NewFile(uintptr(out), tmp)
	fail := func(err error) error {
		_ = dst.Close()
		_ = unix.Unlinkat(dp.fd, tmp, 0)
		return mapErr(err)
	}
	in, err := os.Open(part)
	if err != nil {
		return fail(err)
	}
	_, err = io.Copy(dst, &ctxReader{ctx, in})
	_ = in.Close()
	if err != nil {
		return fail(err)
	}
	if err := unix.Fchmod(out, mode); err != nil {
		return fail(err)
	}
	if uid >= 0 {
		_ = unix.Fchown(out, uid, gid)
	}
	if err := dst.Sync(); err != nil {
		return fail(err)
	}
	_ = dst.Close()
	if err := rename(dp.fd, tmp, dp.base); err != nil {
		_ = unix.Unlinkat(dp.fd, tmp, 0)
		return mapErr(err)
	}
	return mapErr(unix.Unlinkat(pp.fd, pp.base, 0))
}

// UploadCancel abandons an upload and deletes its bytes.
func (s *Service) UploadCancel(ctx context.Context, id string) error {
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()
	if _, err := s.uploadRow(ctx, id); err != nil {
		return err
	}
	_ = os.Remove(s.partPath(id))
	_, err := s.st.DB().ExecContext(ctx, `DELETE FROM fm_uploads WHERE id = ?`, id)
	s.locks.Delete(id)
	return err
}

// SweepUploads removes uploads idle longer than fm.upload.sweep_age. An upload
// that is receiving a chunk or completing holds its lock, and the sweep skips
// it instead of waiting, so a sweep can never delete the file out from under a
// complete in flight.
func (s *Service) SweepUploads(ctx context.Context) (int, error) {
	age := s.limits(ctx).UploadSweepAge
	if age <= 0 {
		return 0, nil
	}
	cutoff := s.now().UTC().Add(-age).Format(timeLayout)
	rows, err := s.st.DB().QueryContext(ctx, `SELECT id FROM fm_uploads WHERE updated_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()

	removed := 0
	for _, id := range ids {
		mu := s.lockFor(id)
		if !mu.TryLock() {
			continue
		}
		// Re-read under the lock: a chunk may have landed since the query.
		var updated string
		err := s.st.DB().QueryRowContext(ctx, `SELECT updated_at FROM fm_uploads WHERE id = ?`, id).Scan(&updated)
		if err == nil && updated < cutoff {
			_ = os.Remove(s.partPath(id))
			if _, err := s.st.DB().ExecContext(ctx, `DELETE FROM fm_uploads WHERE id = ?`, id); err == nil {
				removed++
				s.locks.Delete(id)
			}
		}
		mu.Unlock()
	}

	// Parts with no row (a crash between create and insert) once they are old.
	entries, _ := os.ReadDir(s.uploadDir)
	for _, e := range entries {
		name := e.Name()
		id := strings.TrimSuffix(name, ".part")
		if id == name || !validID(id) {
			continue
		}
		info, err := e.Info()
		if err != nil || s.now().Sub(info.ModTime()) < age+time.Hour {
			continue
		}
		var one int
		if s.st.DB().QueryRowContext(ctx, `SELECT 1 FROM fm_uploads WHERE id = ?`, id).Scan(&one) == sql.ErrNoRows {
			_ = os.Remove(path.Join(s.uploadDir, name))
		}
	}
	return removed, nil
}
