package images

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/docker"
)

// tailLines is how many lines of a build's progress a failed build keeps. The
// whole output goes to the job; this is what the catalogue shows without it.
const tailLines = 30

// Engine is the part of the Docker client a family uses to name, tag and remove
// images. The build itself is a Hook.
type Engine interface {
	ImageInspect(ctx context.Context, ref string) (docker.Image, error)
	ImageTag(ctx context.Context, src, dst string) error
	ImageRemove(ctx context.Context, ref string) error
}

// Hooks are the parts that differ between PHP and nginx.
type Hooks struct {
	// Build makes the image named by b.Tag. It returns when the image exists; an
	// error ends the build.
	Build func(ctx context.Context, b Build, progress docker.Progress) error
	// Check runs the family's validator against the built image, b.Tag. An error
	// means the candidate is not fit to serve; its output is kept with the build.
	// serving says whether a build serves now. When none does, there is nothing
	// to protect yet, and the check may test only what does not depend on
	// configuration that has not been written.
	Check func(ctx context.Context, b Build, serving bool) (string, error)
	// Swap makes the service run the image `to`. `from` is the image serving now,
	// or "" when none does. A service that is not running has nothing to swap and
	// returns nil. When Swap fails it has already put `from` back.
	Swap func(ctx context.Context, from, to string) error
}

// FamilyOptions configures a Family.
type FamilyOptions struct {
	Kind    Kind
	Version string
	// Repo is the repository part of the tag: `panel/php` or `panel/nginx`.
	Repo    string
	Catalog *Catalog
	Engine  Engine
	Hooks   Hooks
	// Log receives what cannot fail a build: a prune that did not remove an image.
	Log *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// Family manages the builds of one image family: one kind and one version. Its
// builds, checks and swaps are serialised; reads are not.
type Family struct {
	o FamilyOptions

	mu   sync.Mutex
	busy bool
}

// NewFamily returns a family, checking that everything it calls is present.
func NewFamily(o FamilyOptions) (*Family, error) {
	switch {
	case o.Kind != KindPHP && o.Kind != KindNginx:
		return nil, fmt.Errorf("images: unknown family %q", o.Kind)
	case o.Version == "" || o.Repo == "":
		return nil, errors.New("images: a family needs a version and a repository")
	case o.Catalog == nil || o.Engine == nil:
		return nil, errors.New("images: a family needs a catalogue and an engine")
	case o.Hooks.Build == nil || o.Hooks.Check == nil || o.Hooks.Swap == nil:
		return nil, errors.New("images: a family needs its build, check and swap hooks")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Family{o: o}, nil
}

// Kind and Version name the family.
func (f *Family) Kind() Kind      { return f.o.Kind }
func (f *Family) Version() string { return f.o.Version }

// Tag is the reference a build of this family is stored under.
func (f *Family) Tag(id string) string { return Tag(f.o.Repo, f.o.Version, id) }

// Idle reports ErrBusy while a build, activation or removal is running.
func (f *Family) Idle() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy {
		return ErrBusy
	}
	return nil
}

func (f *Family) acquire() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy {
		return ErrBusy
	}
	f.busy = true
	return nil
}

func (f *Family) release() {
	f.mu.Lock()
	f.busy = false
	f.mu.Unlock()
}

// Builds lists the family's builds, newest first, with Current and Previous set.
func (f *Family) Builds(ctx context.Context) ([]Build, error) {
	p, err := f.o.Catalog.Pointers(ctx, f.o.Kind, f.o.Version)
	if err != nil {
		return nil, err
	}
	builds, err := f.o.Catalog.list(ctx, f.o.Kind, f.o.Version)
	if err != nil {
		return nil, err
	}
	for i := range builds {
		builds[i].Current = builds[i].ID == p.Current && p.Current != ""
		builds[i].Previous = builds[i].ID == p.Previous && p.Previous != ""
	}
	return builds, nil
}

// Current returns the build the family serves, and false when there is none.
func (f *Family) Current(ctx context.Context) (Build, bool, error) {
	p, err := f.o.Catalog.Pointers(ctx, f.o.Kind, f.o.Version)
	if err != nil || p.Current == "" {
		return Build{}, false, err
	}
	b, err := f.o.Catalog.Get(ctx, p.Current)
	if errors.Is(err, ErrNotFound) {
		return Build{}, false, nil
	}
	if err != nil {
		return Build{}, false, err
	}
	b.Current = true
	return b, true, nil
}

// CurrentTag is the reference the family serves, or "" when there is none.
func (f *Family) CurrentTag(ctx context.Context) (string, error) {
	b, ok, err := f.Current(ctx)
	if err != nil || !ok {
		return "", err
	}
	return b.Tag, nil
}

// Build builds an image, checks it and makes it the one served. A failure
// before the swap leaves the serving image as it was; a failed swap puts the
// previous image back and removes the candidate.
func (f *Family) Build(ctx context.Context, extensions []string, progress docker.Progress) (Build, error) {
	if err := f.acquire(); err != nil {
		return Build{}, err
	}
	defer f.release()

	now := f.o.Now()
	id, err := NewID(now)
	if err != nil {
		return Build{}, err
	}
	b := Build{
		ID: id, Kind: f.o.Kind, Version: f.o.Version, Tag: f.Tag(id),
		Extensions: nonNil(extensions), State: StateBuilding, CreatedAt: now,
	}
	if err := f.o.Catalog.insert(ctx, b); err != nil {
		return b, err
	}
	out := &tail{next: progress}

	// Recording the outcome must survive a canceled job; a build left `building`
	// would block the family until the next boot.
	bg := context.WithoutCancel(ctx)
	fail := func(imageID string, err error) (Build, error) {
		b.State = StateFailed
		b.ImageID = imageID
		b.Output = out.text(err.Error())
		if ferr := f.o.Catalog.finish(bg, b.ID, StateFailed, imageID, b.Output, f.o.Now()); ferr != nil {
			err = errors.Join(err, ferr)
		}
		return b, err
	}

	if err := f.o.Hooks.Build(ctx, b, out.emit); err != nil {
		return fail("", err)
	}
	img, err := f.o.Engine.ImageInspect(ctx, b.Tag)
	if err != nil {
		return fail("", fmt.Errorf("the build finished but %s is not present: %w", b.Tag, err))
	}
	p, err := f.o.Catalog.Pointers(ctx, f.o.Kind, f.o.Version)
	if err != nil {
		return fail(img.ID, err)
	}
	checkOut, err := f.o.Hooks.Check(ctx, b, p.Current != "")
	if err != nil {
		out.add(checkOut)
		f.discard(bg, b.Tag)
		return fail(img.ID, err)
	}
	b.ImageID = img.ID
	if err := f.o.Catalog.finish(bg, b.ID, StateReady, img.ID, "", f.o.Now()); err != nil {
		return b, err
	}
	b.State = StateReady
	return f.promote(ctx, b, true)
}

// Activate makes an existing ready build the one served. It is how a previous
// build is put back.
func (f *Family) Activate(ctx context.Context, id string) (Build, error) {
	if err := f.acquire(); err != nil {
		return Build{}, err
	}
	defer f.release()

	b, err := f.o.Catalog.Get(ctx, id)
	if err != nil {
		return Build{}, err
	}
	if b.Kind != f.o.Kind || b.Version != f.o.Version {
		return Build{}, ErrNotFound
	}
	if b.State != StateReady {
		return Build{}, fmt.Errorf("%w: build %s is %s", ErrNotReady, id, b.State)
	}
	p, err := f.o.Catalog.Pointers(ctx, f.o.Kind, f.o.Version)
	if err != nil {
		return Build{}, err
	}
	if p.Current == id {
		b.Current = true
		return b, nil
	}
	if _, err := f.o.Engine.ImageInspect(ctx, b.Tag); err != nil {
		return Build{}, fmt.Errorf("%w: the image of build %s is gone: %w", ErrNotReady, id, err)
	}
	return f.promote(ctx, b, false)
}

// promote swaps the service onto b and records b as current, the build served
// until now as previous, then prunes. With discard set (a new build), a failed
// swap removes b; an activated build is ready for another attempt and stays.
func (f *Family) promote(ctx context.Context, b Build, discard bool) (Build, error) {
	bg := context.WithoutCancel(ctx)
	p, err := f.o.Catalog.Pointers(ctx, f.o.Kind, f.o.Version)
	if err != nil {
		return b, err
	}
	from := ""
	if p.Current != "" && p.Current != b.ID {
		cur, err := f.o.Catalog.Get(ctx, p.Current)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return b, err
		}
		if err == nil {
			from = cur.Tag
		}
	}

	if err := f.o.Hooks.Swap(ctx, from, b.Tag); err != nil {
		if discard {
			f.discard(bg, b.Tag)
			b.State = StateFailed
			b.Output = fmt.Sprintf("the swap to this build failed; the previous one is serving: %v", err)
			if ferr := f.o.Catalog.finish(bg, b.ID, StateFailed, b.ImageID, b.Output, f.o.Now()); ferr != nil {
				err = errors.Join(err, ferr)
			}
		}
		return b, fmt.Errorf("images: %s %s: swap: %w", f.o.Kind, f.o.Version, err)
	}

	next := Pointers{Current: b.ID, Previous: p.Current}
	if err := f.o.Catalog.setPointers(bg, f.o.Kind, f.o.Version, next); err != nil {
		return b, err
	}
	if err := f.prune(bg, next); err != nil {
		// The swap has happened and is recorded; a failed removal is retried by
		// the next promotion, so it is logged rather than failing this one.
		f.o.Log.WarnContext(bg, "image prune incomplete", "family", f.o.Kind, "version", f.o.Version, "error", err)
	}
	b.Current, b.Previous = true, false
	return b, nil
}

// Remove stops the service through stop, then deletes every image of the family.
// Rows stay, marked pruned, so the history can still be read.
func (f *Family) Remove(ctx context.Context, stop func(ctx context.Context) error) error {
	if err := f.acquire(); err != nil {
		return err
	}
	defer f.release()

	if stop != nil {
		if err := stop(ctx); err != nil {
			return err
		}
	}
	bg := context.WithoutCancel(ctx)
	builds, err := f.o.Catalog.list(ctx, f.o.Kind, f.o.Version)
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range builds {
		if b.State != StateReady {
			continue
		}
		if err := f.removeImage(bg, b.Tag); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := f.o.Catalog.finish(bg, b.ID, StatePruned, b.ImageID, b.Output, f.o.Now()); err != nil {
			errs = append(errs, err)
		}
	}
	if err := f.o.Catalog.setPointers(bg, f.o.Kind, f.o.Version, Pointers{}); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Reconcile corrects the catalogue at daemon start. A build left running is
// failed. When the family has no current build but the image it was built
// from before builds had IDs (legacy) is present, that image is adopted as
// the current build. A current build whose image has gone is dropped from the
// pointers. It reports whether a current build serves.
func (f *Family) Reconcile(ctx context.Context, legacy string) (bool, error) {
	if err := f.acquire(); err != nil {
		return false, err
	}
	defer f.release()

	bg := context.WithoutCancel(ctx)
	stale, err := f.o.Catalog.interrupted(ctx, f.o.Kind, f.o.Version)
	if err != nil {
		return false, err
	}
	for _, b := range stale {
		if err := f.o.Catalog.finish(bg, b.ID, StateFailed, "", "the build was interrupted by a daemon restart", f.o.Now()); err != nil {
			return false, err
		}
	}

	p, err := f.o.Catalog.Pointers(ctx, f.o.Kind, f.o.Version)
	if err != nil {
		return false, err
	}
	if p.Current == "" && legacy != "" {
		adopted, err := f.adopt(ctx, legacy)
		if err != nil {
			return false, err
		}
		if adopted {
			return true, nil
		}
	}
	if p.Current == "" {
		return false, nil
	}
	cur, err := f.o.Catalog.Get(ctx, p.Current)
	if errors.Is(err, ErrNotFound) {
		return false, f.o.Catalog.setPointers(bg, f.o.Kind, f.o.Version, Pointers{Previous: p.Previous})
	}
	if err != nil {
		return false, err
	}
	if _, err := f.o.Engine.ImageInspect(ctx, cur.Tag); err != nil {
		if !errors.Is(err, docker.ErrNotFound) {
			return false, err
		}
		if err := f.o.Catalog.finish(bg, cur.ID, StatePruned, cur.ImageID, "the image was missing at start", f.o.Now()); err != nil {
			return false, err
		}
		return false, f.o.Catalog.setPointers(bg, f.o.Kind, f.o.Version, Pointers{Previous: p.Previous})
	}
	return true, nil
}

// adopt gives a legacy image a build ID and makes it current. The legacy
// reference stays where it is; the build's tag is a second name for the same
// image.
func (f *Family) adopt(ctx context.Context, legacy string) (bool, error) {
	img, err := f.o.Engine.ImageInspect(ctx, legacy)
	if errors.Is(err, docker.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	now := f.o.Now()
	id, err := NewID(now)
	if err != nil {
		return false, err
	}
	b := Build{
		ID: id, Kind: f.o.Kind, Version: f.o.Version, Tag: f.Tag(id), ImageID: img.ID,
		State: StateReady, CreatedAt: now, Output: "adopted from " + legacy,
	}
	if err := f.o.Engine.ImageTag(ctx, legacy, b.Tag); err != nil {
		return false, fmt.Errorf("images: adopt %s: %w", legacy, err)
	}
	if err := f.o.Catalog.insert(ctx, b); err != nil {
		return false, err
	}
	if err := f.o.Catalog.finish(ctx, b.ID, StateReady, img.ID, b.Output, now); err != nil {
		return false, err
	}
	return true, f.o.Catalog.setPointers(ctx, f.o.Kind, f.o.Version, Pointers{Current: id})
}

// prune removes the image of every ready build the family no longer serves or
// keeps as previous. It attempts all of them and reports what failed.
func (f *Family) prune(ctx context.Context, keep Pointers) error {
	builds, err := f.o.Catalog.list(ctx, f.o.Kind, f.o.Version)
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range builds {
		if b.State != StateReady || b.ID == keep.Current || b.ID == keep.Previous {
			continue
		}
		if err := f.removeImage(ctx, b.Tag); err != nil {
			errs = append(errs, fmt.Errorf("build %s: %w", b.ID, err))
			continue
		}
		if err := f.o.Catalog.finish(ctx, b.ID, StatePruned, b.ImageID, b.Output, f.o.Now()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// discard removes a candidate's tag. Failures are logged: the row that refers to
// it is marked failed either way.
func (f *Family) discard(ctx context.Context, tag string) {
	if err := f.removeImage(ctx, tag); err != nil {
		f.o.Log.WarnContext(ctx, "could not remove a rejected image", "tag", tag, "error", err)
	}
}

// removeImage deletes a tag. A tag that is already gone is not an error.
func (f *Family) removeImage(ctx context.Context, tag string) error {
	err := f.o.Engine.ImageRemove(ctx, tag)
	if err != nil && !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	return nil
}

// tail forwards progress to the job and keeps its last lines for a failed build.
type tail struct {
	next  docker.Progress
	lines []string
}

func (t *tail) emit(line string) {
	t.add(line)
	if t.next != nil {
		t.next(line)
	}
}

func (t *tail) add(text string) {
	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		t.lines = append(t.lines, line)
	}
	if n := len(t.lines); n > tailLines {
		t.lines = t.lines[n-tailLines:]
	}
}

// text is the kept tail followed by the reason the build failed.
func (t *tail) text(reason string) string {
	return strings.Join(append(append([]string(nil), t.lines...), reason), "\n")
}
