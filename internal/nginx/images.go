package nginx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
)

// ErrSameVersion is a switch to the version that already serves.
var ErrSameVersion = errors.New("nginx: that version already serves")

// Images owns the nginx builds: each build is kept in its own directory under
// VersionsDir, one build serves, and the build served before is kept so one action
// can put it back.
//
// The bookkeeping (build IDs, which one is current, which was previous, pruning)
// is package images, shared with PHP. What differs is what a build is: here a
// directory made by the builder, and "serving" means the CurrentLink symlink names
// it and the unit has been restarted onto it.
//
// There is one family of builds per nginx version, and one version is active.
type Images struct {
	p   Paths
	run Runner
	b   Builder
	cat *images.Catalog
	log *slog.Logger

	mu     sync.Mutex
	fams   map[string]*images.Family
	fam    *images.Family // the active family
	active string
}

// NewImages returns the nginx build family over the catalogue.
func NewImages(p Paths, run Runner, b Builder, cat *images.Catalog, log *slog.Logger) (*Images, error) {
	i := &Images{p: p, run: run, b: b, cat: cat, log: log, fams: map[string]*images.Family{}, active: Version}
	// The version chosen last time, kept across restarts. A stored value that no
	// longer parses is ignored: the default family always exists.
	if v, ok, err := cat.ActiveVersion(context.Background(), images.KindNginx); err == nil && ok {
		if v = NormalizeVersion(v); CheckVersion(v) == nil {
			i.active = v
		}
	}
	fam, err := i.family(i.active)
	if err != nil {
		return nil, err
	}
	i.fam = fam
	return i, nil
}

// family returns the family of one version, creating it on first use.
func (i *Images) family(version string) (*images.Family, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if f, ok := i.fams[version]; ok {
		return f, nil
	}
	fam, err := images.NewFamily(images.FamilyOptions{
		Kind: images.KindNginx, Version: version, Repo: Repo, Catalog: i.cat, Engine: dirStore{i.p}, Log: i.log,
		Hooks: images.Hooks{
			Build: func(ctx context.Context, b images.Build, progress docker.Progress) error {
				return i.b.Build(ctx, b.ID, version, progress)
			},
			Check: func(ctx context.Context, b images.Build, serving bool) (string, error) {
				// The first build is made while the panel is still writing nginx's
				// config files, so `nginx -t` would fail on files that do not exist
				// yet. On a host upgraded from the container runtime the files exist
				// but still name paths that only the image had; the reconcile
				// rewrites them after this build, and the validation that follows
				// the rewrite is the first real test. Once a native build serves, the
				// live configuration is checked against every candidate, whether or
				// not its family serves: that is what makes a version switch safe.
				if !serving && (!i.configPresent() || !i.anyServes()) {
					return "", nil
				}
				return checkCandidate(ctx, i.run, i.p, b.ID)
			},
			Swap: i.swap,
		},
	})
	if err != nil {
		return nil, err
	}
	i.fams[version] = fam
	return fam, nil
}

// anyServes reports whether a native build serves: the current link exists.
func (i *Images) anyServes() bool {
	_, err := readLink(i.p)
	return err == nil
}

func (i *Images) configPresent() bool {
	_, err := os.Stat(filepath.FromSlash(i.p.ConfPath()))
	return err == nil
}

// Version is the nginx version that serves.
func (i *Images) Version() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.active
}

func (i *Images) activeFamily() *images.Family {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.fam
}

// Idle reports images.ErrBusy while a build, update or rollback is running, in
// any version.
func (i *Images) Idle() error {
	i.mu.Lock()
	fams := make([]*images.Family, 0, len(i.fams))
	for _, f := range i.fams {
		fams = append(fams, f)
	}
	i.mu.Unlock()
	for _, f := range fams {
		if err := f.Idle(); err != nil {
			return err
		}
	}
	return nil
}

// Builds lists the active version's builds, newest first, flagged with what
// serves and what served before.
func (i *Images) Builds(ctx context.Context) ([]images.Build, error) {
	return i.activeFamily().Builds(ctx)
}

// Current returns the build that serves, and false when none does.
func (i *Images) Current(ctx context.Context) (images.Build, bool, error) {
	return i.activeFamily().Current(ctx)
}

// Present reports whether a build serves and its files are on the host.
func (i *Images) Present(ctx context.Context) (bool, error) {
	tag, err := i.activeFamily().CurrentTag(ctx)
	if err != nil || tag == "" {
		return false, err
	}
	if _, err := (dirStore{i.p}).ImageInspect(ctx, tag); err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Build compiles a new build from the newest sources, checks it with `nginx -t`
// against the live configuration, and makes it the one that serves. It is the
// first build and the update alike.
func (i *Images) Build(ctx context.Context, progress docker.Progress) (images.Build, error) {
	return i.activeFamily().Build(ctx, nil, progress)
}

// Activate makes an earlier ready build serve: the rollback.
func (i *Images) Activate(ctx context.Context, id string) (images.Build, error) {
	return i.activeFamily().Activate(ctx, id)
}

// SetVersion makes another nginx version serve. A version that was built before
// and whose files are still here is reused; otherwise it is compiled. Either way
// the live configuration is checked against it before nginx is touched, and a
// failed swap puts the old build back. The new version is recorded only once it
// serves.
func (i *Images) SetVersion(ctx context.Context, version string, progress docker.Progress) (images.Build, error) {
	if err := CheckVersion(version); err != nil {
		return images.Build{}, err
	}
	if version == i.Version() {
		return images.Build{}, ErrSameVersion
	}
	if err := i.Idle(); err != nil {
		return images.Build{}, err
	}
	fam, err := i.family(version)
	if err != nil {
		return images.Build{}, err
	}

	var built images.Build
	cur, has, err := fam.Current(ctx)
	if err != nil {
		return images.Build{}, err
	}
	reuse := false
	if has && cur.State == images.StateReady {
		if _, ierr := (dirStore{i.p}).ImageInspect(ctx, cur.Tag); ierr == nil {
			reuse = true
		}
	}
	if reuse {
		// The family already points at this build, so Activate would not swap:
		// check and swap explicitly.
		if out, cerr := checkCandidate(ctx, i.run, i.p, cur.ID); cerr != nil {
			return images.Build{}, fmt.Errorf("nginx %s does not accept the current configuration: %w\n%s", version, cerr, out)
		}
		from, _ := i.activeFamily().CurrentTag(ctx)
		if err = i.swap(ctx, from, cur.Tag); err == nil {
			built = cur
		}
	} else {
		built, err = fam.Build(ctx, nil, progress)
	}
	if err != nil {
		return built, err
	}

	if err := i.cat.SetActiveVersion(context.WithoutCancel(ctx), images.KindNginx, version); err != nil {
		return built, fmt.Errorf("nginx %s serves, but remembering the choice failed: %w", version, err)
	}
	i.mu.Lock()
	i.active, i.fam = version, fam
	i.mu.Unlock()
	return built, nil
}

// Reconcile is the boot step: it fails any build left running, drops a current
// build whose files have gone, and points the CurrentLink at the build the
// catalogue says serves. It reports whether a build serves.
func (i *Images) Reconcile(ctx context.Context) (bool, error) {
	ok, err := i.activeFamily().Reconcile(ctx, "")
	if err != nil || !ok {
		return ok, err
	}
	cur, has, err := i.activeFamily().Current(ctx)
	if err != nil || !has {
		return false, err
	}
	if got, lerr := readLink(i.p); lerr != nil || got != cur.ID {
		if err := setLink(i.p, cur.ID); err != nil {
			return false, err
		}
	}
	return true, nil
}

// swap makes nginx run the build `to`. `from` is the build serving now, or "" when
// none does. The link is switched first; a running nginx is then restarted onto
// it, since a binary cannot be swapped under a reload. A nginx that is not
// running has nothing to restart: the boot reconcile starts it on the new link.
// When the restart does not hold, the link is put back and nginx restarted on the
// old build, so a failed swap leaves what served before serving.
func (i *Images) swap(ctx context.Context, from, to string) error {
	toID, err := idOfTag(to)
	if err != nil {
		return err
	}
	prev, _ := readLink(i.p)
	if prev == "" && from != "" {
		prev, _ = idOfTag(from)
	}
	if err := setLink(i.p, toID); err != nil {
		return err
	}
	st, err := status(ctx, i.run)
	if err != nil || !st.Active {
		return nil
	}
	if err := restartUnit(ctx, i.run); err != nil {
		if prev == "" {
			return err
		}
		bg := context.WithoutCancel(ctx)
		if lerr := setLink(i.p, prev); lerr != nil {
			return errors.Join(err, lerr)
		}
		if rerr := restartUnit(bg, i.run); rerr != nil {
			return errors.Join(err, fmt.Errorf("putting build %s back: %w", prev, rerr))
		}
		return err
	}
	return nil
}
