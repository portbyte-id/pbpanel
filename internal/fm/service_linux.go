//go:build linux

package fm

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/portbyte-id/server-panel/internal/store"
)

// Options configure a Service.
type Options struct {
	Store *store.Store
	// PanelDir is the panel tree (for example /www/panel). The trash and uploads
	// directories are relative to it.
	PanelDir string
	// Limits is read on every operation. Nil means DefaultLimits.
	Limits func(context.Context) Limits
	Now    func() time.Time
}

// Service is the file manager: the descriptor-based FS plus the state that
// lives in the panel's database (trash, uploads) and the guardrails that need
// the panel's layout.
type Service struct {
	*FS
	st        *store.Store
	panel     string
	trashDir  string
	uploadDir string
	limits    func(context.Context) Limits
	now       func() time.Time

	locks sync.Map // upload id -> *sync.Mutex
}

// New builds a Service and creates the trash and uploads directories.
func New(o Options) (*Service, error) {
	if o.Store == nil {
		return nil, errors.New("fm: Options.Store is required")
	}
	panel := path.Clean(o.PanelDir)
	if !path.IsAbs(panel) {
		return nil, fmt.Errorf("fm: PanelDir %q must be absolute", o.PanelDir)
	}
	s := &Service{
		FS: NewFS(NewPolicy(panel)), st: o.Store, panel: panel,
		trashDir:  path.Join(panel, "trash"),
		uploadDir: path.Join(panel, "uploads"),
		limits:    o.Limits, now: o.Now,
	}
	// The trash and uploads trees are reachable only through the Trash and
	// Upload APIs: a rename in there would orphan an index row. The guard runs
	// on the resolved path of a create target or a move/copy destination.
	s.guard = func(real string) error {
		if s.internalTree(real) {
			return fmt.Errorf("%w: %s is managed by the panel", ErrProtected, real)
		}
		return nil
	}
	if s.limits == nil {
		s.limits = func(context.Context) Limits { return DefaultLimits() }
	}
	if s.now == nil {
		s.now = time.Now
	}
	for _, d := range []string{s.trashDir, s.uploadDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("fm: %w", err)
		}
	}
	return s, nil
}

// Clean applies the policy's path rules to a client path.
func (s *Service) Clean(p string) (string, error) { return s.pol.Clean(p) }

// internalTree reports a path inside the trash or uploads directory, which only
// the Trash and Upload APIs may touch: a rename in there would orphan an index
// row.
func (s *Service) internalTree(real string) bool {
	return Within(s.trashDir, real) || Within(s.uploadDir, real)
}

// SameDevice reports whether two existing paths live on one filesystem. It is
// how callers decide between a quick rename and a copy that needs a job.
func (s *Service) SameDevice(a, b string) bool {
	var sa, sb unix.Stat_t
	if unix.Stat(a, &sa) != nil || unix.Stat(b, &sb) != nil {
		return false
	}
	return sa.Dev == sb.Dev
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

const timeLayout = "2006-01-02T15:04:05.000Z"

// --- trash ---------------------------------------------------------------

// TrashNeedsJob reports a delete that will copy rather than rename, which can
// take long enough to belong in a job.
func (s *Service) TrashNeedsJob(raw string) bool {
	clean, err := s.pol.Clean(raw)
	if err != nil {
		return false
	}
	return !s.SameDevice(path.Dir(clean), s.trashDir)
}

// Trash moves a path to the recycle bin. A directory needs its base name typed
// as confirm. The hard-deny list applies to the real, resolved path.
func (s *Service) Trash(ctx context.Context, raw, confirm string, progress Progress) (TrashItem, error) {
	clean, err := s.pol.Clean(raw)
	if err != nil {
		return TrashItem{}, err
	}
	p, err := s.parentOf(clean)
	if err != nil {
		return TrashItem{}, err
	}
	defer p.close()
	real := p.full()
	if err := s.pol.CheckDestructive(real); err != nil {
		return TrashItem{}, err
	}
	if s.internalTree(real) {
		return TrashItem{}, fmt.Errorf("%w: use the Trash view for %s", ErrProtected, real)
	}
	st, err := lstatAt(p.fd, p.base)
	if err != nil {
		return TrashItem{}, err
	}
	if isDir(st) {
		if err := s.pol.CheckConfirm(real, confirm); err != nil {
			return TrashItem{}, err
		}
	}
	size, err := treeSize(ctx, p.fd, p.base, st)
	if err != nil {
		return TrashItem{}, err
	}
	lim := s.limits(ctx)
	if lim.TrashMaxBytes > 0 && size > lim.TrashMaxBytes {
		return TrashItem{}, fmt.Errorf("%w: %d bytes is over the trash limit of %d; delete it permanently instead", ErrTooLarge, size, lim.TrashMaxBytes)
	}

	item := TrashItem{
		ID: newID(), Name: p.base, OrigPath: real, Size: size, IsDir: isDir(st),
		Mode: st.Mode & 0o7777, UID: st.Uid, GID: st.Gid, DeletedAt: s.now().UTC(),
	}
	slot := path.Join(s.trashDir, item.ID)
	if err := os.Mkdir(slot, 0o700); err != nil {
		return TrashItem{}, err
	}
	if _, err := s.st.DB().ExecContext(ctx,
		`INSERT INTO fm_trash (id, name, orig_path, size, is_dir, mode, uid, gid, deleted_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		item.ID, item.Name, item.OrigPath, item.Size, boolInt(item.IsDir), item.Mode, item.UID, item.GID,
		item.DeletedAt.Format(timeLayout)); err != nil {
		_ = os.Remove(slot)
		return TrashItem{}, err
	}
	if err := s.moveInto(ctx, p, slot, p.base, progress); err != nil {
		_, _ = s.st.DB().ExecContext(context.WithoutCancel(ctx), `DELETE FROM fm_trash WHERE id = ?`, item.ID)
		_ = os.RemoveAll(slot)
		return TrashItem{}, err
	}
	_, _ = s.Purge(ctx) // keep the bin inside its limits; failures are not the delete's
	return item, nil
}

// moveInto renames src into dir under name, copying then removing across
// filesystems and preserving owner, mode and times.
func (s *Service) moveInto(ctx context.Context, src *parent, dir, name string, progress Progress) error {
	dp, err := s.parentOf(path.Join(dir, name))
	if err != nil {
		return err
	}
	defer dp.close()
	err = renameNoReplace(src.fd, src.base, dp.fd, dp.base)
	if !errors.Is(err, unix.EXDEV) {
		return mapErr(err)
	}
	progress.say("copying %s to %s (different filesystem)", src.full(), dir)
	if err := s.copyTree(ctx, src, dp, true, progress); err != nil {
		_ = s.removeTree(context.WithoutCancel(ctx), dp.fd, dp.base, nil)
		return err
	}
	return s.removeTree(ctx, src.fd, src.base, progress)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func scanTrash(sc interface{ Scan(...any) error }) (TrashItem, error) {
	var (
		it  TrashItem
		dir int
		at  string
	)
	if err := sc.Scan(&it.ID, &it.Name, &it.OrigPath, &it.Size, &dir, &it.Mode, &it.UID, &it.GID, &at); err != nil {
		return it, err
	}
	it.IsDir = dir != 0
	it.DeletedAt, _ = time.Parse(timeLayout, at)
	return it, nil
}

const trashCols = `id, name, orig_path, size, is_dir, mode, uid, gid, deleted_at`

// TrashList returns the bin, newest first.
func (s *Service) TrashList(ctx context.Context) ([]TrashItem, error) {
	rows, err := s.st.DB().QueryContext(ctx, `SELECT `+trashCols+` FROM fm_trash ORDER BY deleted_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []TrashItem{}
	for rows.Next() {
		it, err := scanTrash(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// TrashGet returns one bin entry or ErrNotFound.
func (s *Service) TrashGet(ctx context.Context, id string) (TrashItem, error) {
	it, err := scanTrash(s.st.DB().QueryRowContext(ctx, `SELECT `+trashCols+` FROM fm_trash WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return it, ErrNotFound
	}
	return it, err
}

// payload is where an entry's bytes sit in the bin. The id is hex we generated,
// but it is still checked: a row must never name a path outside the bin.
func (s *Service) payload(it TrashItem) (string, error) {
	if len(it.ID) != 32 || CheckName(it.Name) != nil {
		return "", fmt.Errorf("%w: corrupt trash entry", ErrInvalidPath)
	}
	for _, c := range it.ID {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("%w: corrupt trash entry", ErrInvalidPath)
		}
	}
	return path.Join(s.trashDir, it.ID, it.Name), nil
}

// RestoreNeedsJob reports a restore that has to copy.
func (s *Service) RestoreNeedsJob(ctx context.Context, id string) bool {
	it, err := s.TrashGet(ctx, id)
	if err != nil {
		return false
	}
	return !s.SameDevice(path.Join(s.trashDir, it.ID), path.Dir(it.OrigPath))
}

// Restore puts an entry back where it came from, or under newName in the same
// directory when the original name is taken. It never overwrites.
func (s *Service) Restore(ctx context.Context, id, newName string, progress Progress) (string, error) {
	it, err := s.TrashGet(ctx, id)
	if err != nil {
		return "", err
	}
	src, err := s.payload(it)
	if err != nil {
		return "", err
	}
	name := path.Base(it.OrigPath)
	if newName != "" {
		if err := CheckName(newName); err != nil {
			return "", err
		}
		name = newName
	}
	dest := path.Join(path.Dir(it.OrigPath), name)
	dp, err := s.parentOf(dest)
	if err != nil {
		return "", err
	}
	defer dp.close()
	if err := s.pol.CheckWrite(dp.full()); err != nil {
		return "", err
	}
	if _, err := lstatAt(dp.fd, dp.base); err == nil {
		return "", fmt.Errorf("%w: %s; restore under another name", ErrExists, dp.full())
	}
	sp, err := s.parentOf(src)
	if err != nil {
		return "", err
	}
	defer sp.close()
	err = renameNoReplace(sp.fd, sp.base, dp.fd, dp.base)
	if errors.Is(err, unix.EXDEV) {
		progress.say("copying back to %s (different filesystem)", dp.full())
		if err := s.copyTree(ctx, sp, dp, true, progress); err != nil {
			_ = s.removeTree(context.WithoutCancel(ctx), dp.fd, dp.base, nil)
			return "", err
		}
		err = s.removeTree(ctx, sp.fd, sp.base, progress)
	}
	if err != nil {
		return "", mapErr(err)
	}
	_ = os.Remove(path.Join(s.trashDir, it.ID))
	_, err = s.st.DB().ExecContext(ctx, `DELETE FROM fm_trash WHERE id = ?`, id)
	return dp.full(), err
}

// DeletePermanently removes a bin entry for good. The entry's name must be
// typed: this is the one action here with no way back.
func (s *Service) DeletePermanently(ctx context.Context, id, confirm string, progress Progress) error {
	it, err := s.TrashGet(ctx, id)
	if err != nil {
		return err
	}
	if confirm != it.Name {
		return fmt.Errorf("%w: type the name %q to confirm", ErrConfirm, it.Name)
	}
	return s.dropTrash(ctx, it, progress)
}

func (s *Service) dropTrash(ctx context.Context, it TrashItem, progress Progress) error {
	if _, err := s.payload(it); err != nil {
		return err
	}
	slot := path.Join(s.trashDir, it.ID)
	if err := s.RemoveTree(ctx, slot, progress); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	_, err := s.st.DB().ExecContext(ctx, `DELETE FROM fm_trash WHERE id = ?`, it.ID)
	return err
}

// Purge deletes entries past the retention period, then the oldest ones until
// the bin fits its size limit. It returns how many entries it removed.
func (s *Service) Purge(ctx context.Context) (int, error) {
	lim := s.limits(ctx)
	items, err := s.TrashList(ctx) // newest first
	if err != nil {
		return 0, err
	}
	cutoff := s.now().UTC().Add(-time.Duration(lim.TrashRetentionDays) * 24 * time.Hour)
	var keep []TrashItem
	removed := 0
	var firstErr error
	drop := func(it TrashItem) {
		if err := s.dropTrash(ctx, it, nil); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return
		}
		removed++
	}
	for _, it := range items {
		if lim.TrashRetentionDays > 0 && it.DeletedAt.Before(cutoff) {
			drop(it)
		} else {
			keep = append(keep, it)
		}
	}
	if lim.TrashMaxBytes > 0 {
		var total int64
		for _, it := range keep {
			total += it.Size
		}
		for i := len(keep) - 1; i >= 0 && total > lim.TrashMaxBytes; i-- { // oldest last
			total -= keep[i].Size
			drop(keep[i])
		}
	}
	return removed, firstErr
}

// RunPurgeLoop purges once now and then daily until ctx ends. onError may be nil.
func (s *Service) RunPurgeLoop(ctx context.Context, onError func(error)) {
	tick := time.NewTicker(24 * time.Hour)
	defer tick.Stop()
	for {
		if _, err := s.Purge(ctx); err != nil && onError != nil {
			onError(err)
		}
		if _, err := s.SweepUploads(ctx); err != nil && onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// PreflightTrash runs the guardrails of Trash without moving anything, so the
// API can refuse a protected or unconfirmed delete with a status code before it
// has started a job.
func (s *Service) PreflightTrash(clean, confirm string) error {
	p, err := s.parentOf(clean)
	if err != nil {
		return err
	}
	defer p.close()
	real := p.full()
	if err := s.pol.CheckDestructive(real); err != nil {
		return err
	}
	if s.internalTree(real) {
		return fmt.Errorf("%w: use the Trash view for %s", ErrProtected, real)
	}
	st, err := lstatAt(p.fd, p.base)
	if err != nil {
		return err
	}
	if isDir(st) {
		return s.pol.CheckConfirm(real, confirm)
	}
	return nil
}
