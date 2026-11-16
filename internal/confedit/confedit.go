package confedit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

const (
	// MaxFileBytes is the largest file an apply reads or writes. A managed
	// config is a few kilobytes; the limit keeps a mistaken path (a log, an
	// archive) from being loaded into memory and copied into the history.
	MaxFileBytes = 2 << 20

	// HistoryVersions is how many versions of each file are kept (plan.md §3.1).
	HistoryVersions = 10

	// defaultFileMode is for a file an apply creates. Config files are read by
	// service containers running as other users, so they are world-readable;
	// nothing secret is written through this package.
	defaultFileMode fs.FileMode = 0o644
	dirMode         fs.FileMode = 0o755

	// rollbackTimeout bounds the reloads done while rolling back. The apply's
	// own context may already be canceled, which is no reason to leave a
	// service on a config that was just withdrawn.
	rollbackTimeout = 60 * time.Second
)

// Service is something that reads the files being changed: nginx, one PHP-FPM
// version, supervisord.
type Service interface {
	// Name identifies the service in errors and keys its apply lock.
	Name() string
	// Validate checks the config now on disk (`nginx -t`). On rejection it
	// returns the validator's output verbatim and a non-nil error.
	Validate(ctx context.Context) (output string, err error)
	// Reload makes the running service pick up the config on disk.
	Reload(ctx context.Context) error
	// Healthy reports whether the service is up after a reload.
	Healthy(ctx context.Context) error
}

// File is one file an apply writes or removes.
type File struct {
	// Path is absolute and inside one of the editor's roots.
	Path    string
	Content []byte
	// Delete removes the file instead of writing Content.
	Delete bool
	// Mode is used only when the file does not exist yet; zero means 0644. An
	// existing file keeps its mode and owner.
	Mode fs.FileMode
}

// Change is one all-or-nothing set of file writes.
type Change struct {
	Files []File
	// Services are validated, then reloaded, then health-checked, each pass in
	// this order. The order matters when one service depends on another having
	// reloaded first (a PHP version switch reloads the new FPM before nginx).
	Services []Service
	// Reason is recorded with the history entries, e.g. "edit site a.test".
	Reason string
}

// Stage is where an apply failed.
type Stage string

// Apply stages, in order.
const (
	StagePrepare  Stage = "prepare"
	StageWrite    Stage = "write"
	StageValidate Stage = "validate"
	StageReload   Stage = "reload"
	StageHealth   Stage = "health"
)

// ApplyError is a failed apply. Unless RollbackErr is set, every file is back
// to its previous content.
type ApplyError struct {
	Stage Stage
	// Service is the service that rejected the change, when one did.
	Service string
	// Output is the validator's output, verbatim, for the UI.
	Output string
	Err    error
	// RollbackErr is set when putting the previous state back failed too. The
	// journal is then left in place for Recover.
	RollbackErr error
}

func (e *ApplyError) Error() string {
	var b strings.Builder
	b.WriteString("confedit: " + string(e.Stage))
	if e.Service != "" {
		b.WriteString(" " + e.Service)
	}
	b.WriteString(": " + e.Err.Error())
	if out := strings.TrimSpace(e.Output); out != "" {
		b.WriteString(": " + out)
	}
	if e.RollbackErr != nil {
		b.WriteString(" (rollback FAILED: " + e.RollbackErr.Error() + ")")
	} else if e.Stage != StagePrepare {
		b.WriteString(" (previous files restored)")
	}
	return b.String()
}

func (e *ApplyError) Unwrap() error { return e.Err }

// Result is a successful apply.
type Result struct {
	// Versions maps each written path to the history version of its new content.
	Versions map[string]int
	// Warnings are problems that did not stop the apply: the config is live,
	// but, for instance, its history entry could not be written.
	Warnings []string
}

// Options configures an Editor.
type Options struct {
	// Roots are the directories managed files may live in. A path outside all
	// of them is refused, so a bug in a caller cannot turn the root daemon's
	// config writer onto an arbitrary file.
	Roots []string
	// HistoryDir holds version blobs and the in-flight journal
	// (/www/panel/history).
	HistoryDir string
	// Store indexes the history.
	Store *store.Store
}

// Editor applies changes. It is safe for concurrent use; applies that share a
// service run one at a time.
type Editor struct {
	roots      []string
	historyDir string
	store      *store.Store
	now        func() time.Time
	write      writeFunc

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New returns an Editor.
func New(opts Options) (*Editor, error) {
	if opts.Store == nil {
		return nil, errors.New("confedit: store is nil")
	}
	if len(opts.Roots) == 0 {
		return nil, errors.New("confedit: at least one root is required")
	}
	e := &Editor{store: opts.Store, now: time.Now, write: writeAtomic, locks: map[string]*sync.Mutex{}}
	for _, r := range append([]string{opts.HistoryDir}, opts.Roots...) {
		if !filepath.IsAbs(r) || filepath.Clean(r) != r {
			return nil, fmt.Errorf("confedit: %q must be a clean absolute path", r)
		}
	}
	e.roots = append(e.roots, opts.Roots...)
	e.historyDir = opts.HistoryDir
	// The history is not a place managed config lives; a root that contained
	// it would let an apply overwrite its own journal.
	for _, r := range e.roots {
		if within(e.historyDir, r) || within(r, e.historyDir) {
			return nil, fmt.Errorf("confedit: history directory %s and root %s must not contain one another", e.historyDir, r)
		}
	}
	return e, nil
}

// within reports whether path is dir or lies inside it, lexically.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// Apply writes the change, validates, reloads and health-checks its services,
// and rolls every file back if any step fails. A failure is always an
// *ApplyError.
func (e *Editor) Apply(ctx context.Context, ch Change) (Result, error) {
	prepErr := func(err error) (Result, error) {
		return Result{}, &ApplyError{Stage: StagePrepare, Err: err}
	}
	if err := e.check(ch); err != nil {
		return prepErr(err)
	}

	unlock := e.lock(ch.Services)
	defer unlock()

	snaps := make([]snapshot, len(ch.Files))
	for i, f := range ch.Files {
		s, err := takeSnapshot(f.Path, MaxFileBytes)
		if err != nil {
			return prepErr(err)
		}
		snaps[i] = s
	}
	newDirs := missingDirs(ch.Files)
	journal, err := e.writeJournal(snaps, newDirs)
	if err != nil {
		return prepErr(fmt.Errorf("write journal: %w", err))
	}

	var reloaded []Service
	fail := func(stage Stage, svc Service, output string, cause error) (Result, error) {
		ae := &ApplyError{Stage: stage, Output: output, Err: cause}
		if svc != nil {
			ae.Service = svc.Name()
		}
		ae.RollbackErr = e.rollback(ctx, snaps, newDirs, reloaded, journal)
		return Result{}, ae
	}

	for i := len(newDirs) - 1; i >= 0; i-- { // shallowest first
		if err := os.Mkdir(newDirs[i], dirMode); err != nil && !errors.Is(err, fs.ErrExist) {
			return fail(StageWrite, nil, "", err)
		}
	}
	for i, f := range ch.Files {
		if err := e.writeOne(f, snaps[i]); err != nil {
			return fail(StageWrite, nil, "", err)
		}
	}
	for _, svc := range ch.Services {
		if out, err := svc.Validate(ctx); err != nil {
			return fail(StageValidate, svc, out, err)
		}
	}
	for _, svc := range ch.Services {
		// Recorded before the call: a reload that failed may still have been
		// partly taken, so the rollback reloads this service again too.
		reloaded = append(reloaded, svc)
		if err := svc.Reload(ctx); err != nil {
			return fail(StageReload, svc, "", err)
		}
	}
	for _, svc := range ch.Services {
		if err := svc.Healthy(ctx); err != nil {
			return fail(StageHealth, svc, "", err)
		}
	}

	res := Result{Versions: map[string]int{}}
	if err := os.RemoveAll(journal); err != nil {
		// Left behind, the journal would undo this apply at the next boot.
		// That is the safe direction, but the operator must hear about it.
		res.Warnings = append(res.Warnings, fmt.Sprintf("the apply journal %s could not be removed and would revert this change at the next daemon start: %v", journal, err))
	}
	e.recordHistory(ctx, ch, snaps, &res)
	return res, nil
}

// check validates the shape of a change before anything is locked or read.
func (e *Editor) check(ch Change) error {
	if len(ch.Files) == 0 {
		return errors.New("no files in the change")
	}
	seenFiles := map[string]bool{}
	for _, f := range ch.Files {
		if err := e.checkPath(f.Path); err != nil {
			return err
		}
		if seenFiles[f.Path] {
			return fmt.Errorf("%s is listed twice", f.Path)
		}
		seenFiles[f.Path] = true
		if len(f.Content) > MaxFileBytes {
			return fmt.Errorf("%s: %d bytes is above the %d byte limit for a managed config", f.Path, len(f.Content), MaxFileBytes)
		}
		if f.Delete && len(f.Content) > 0 {
			return fmt.Errorf("%s: a file cannot be both deleted and written", f.Path)
		}
	}
	seenServices := map[string]bool{}
	for _, svc := range ch.Services {
		if svc == nil {
			return errors.New("a service in the change is nil")
		}
		if seenServices[svc.Name()] {
			return fmt.Errorf("service %s is listed twice", svc.Name())
		}
		seenServices[svc.Name()] = true
	}
	return nil
}

// checkPath refuses any path that is not a file strictly inside a root, both as
// written and after symlinks in its existing parent directories are resolved.
func (e *Editor) checkPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%q must be a clean absolute path", path)
	}
	var root string
	for _, r := range e.roots {
		if path != r && within(path, r) {
			root = r
			break
		}
	}
	if root == "" {
		return fmt.Errorf("%s is outside the managed config directories", path)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("managed directory %s: %w", root, err)
	}
	// The deepest directory that exists today is the one a symlink could have
	// redirected; anything below it will be created by this apply as a real
	// directory.
	dir := filepath.Dir(path)
	for {
		if _, err := os.Lstat(dir); err == nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	if !within(realDir, realRoot) {
		return fmt.Errorf("%s resolves to %s, outside %s", dir, realDir, root)
	}
	return nil
}

// missingDirs lists the directories the change has to create, deepest first,
// which is the order a rollback removes them in.
func missingDirs(files []File) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		if f.Delete {
			continue
		}
		for dir := filepath.Dir(f.Path); !seen[dir]; dir = filepath.Dir(dir) {
			if _, err := os.Lstat(dir); err == nil {
				break
			}
			seen[dir] = true
			out = append(out, dir)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func (e *Editor) writeOne(f File, prev snapshot) error {
	if f.Delete {
		if err := os.Remove(f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	mode, own := f.Mode.Perm(), owner{}
	if prev.existed {
		mode, own = prev.mode, prev.owner
	} else if mode == 0 {
		mode = defaultFileMode
	}
	return e.write(f.Path, f.Content, mode, own)
}

// rollback restores every snapshot, removes the directories the apply created,
// and reloads the services that had been told to reload. It returns nil only
// when every file is back; the journal is then removed.
func (e *Editor) rollback(ctx context.Context, snaps []snapshot, newDirs []string, reloaded []Service, journal string) error {
	var errs []error
	for _, s := range snaps {
		if err := s.restore(e.write); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", s.path, err))
		}
	}
	for _, dir := range newDirs { // deepest first
		if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove %s: %w", dir, err))
		}
	}
	if len(errs) == 0 {
		if err := os.RemoveAll(journal); err != nil {
			errs = append(errs, fmt.Errorf("remove journal: %w", err))
		}
	}

	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	for _, svc := range reloaded {
		if err := svc.Reload(rctx); err != nil {
			errs = append(errs, fmt.Errorf("reload %s onto the restored config: %w", svc.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// lock takes the apply lock of every service in the change, in name order so
// two applies that share services cannot deadlock. A change with no service
// still serialises with other such changes.
func (e *Editor) lock(services []Service) (unlock func()) {
	names := make([]string, 0, len(services))
	for _, svc := range services {
		names = append(names, svc.Name())
	}
	if len(names) == 0 {
		names = []string{""}
	}
	sort.Strings(names)

	held := make([]*sync.Mutex, 0, len(names))
	for _, name := range names {
		e.mu.Lock()
		m := e.locks[name]
		if m == nil {
			m = &sync.Mutex{}
			e.locks[name] = m
		}
		e.mu.Unlock()
		m.Lock()
		held = append(held, m)
	}
	return func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i].Unlock()
		}
	}
}
