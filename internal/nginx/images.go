package nginx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sync"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
)

// SuggestedVersions are the official nginx tags the Nginx page offers, newest
// first. Any MAJOR.MINOR tag Docker Hub has works; a tag that does not exist fails
// at the pull, before anything running is touched.
var SuggestedVersions = []string{"1.30", "1.29", "1.28"}

var versionPattern = regexp.MustCompile(`^[1-9][0-9]?\.[0-9]{1,2}$`)

// CheckVersion reports whether v is a tag the panel will build from.
func CheckVersion(v string) error {
	if !versionPattern.MatchString(v) {
		return fmt.Errorf("nginx: version %q must look like 1.29", v)
	}
	return nil
}

// BaseImageFor is the official image a version is built on.
func BaseImageFor(version string) string { return "nginx:" + version }

// ErrSameVersion is a switch to the version that already serves.
var ErrSameVersion = errors.New("nginx: that version already serves")

// Images owns the nginx image builds (development plan M8, steps 8.1 and 8.6):
// each build of the image is kept under its own tag, one build serves, and the
// build served before is kept so one action can put it back.
//
// There is one family of builds per nginx version, and one version is active.
// Switching version builds (or reuses) the target family's image, checks the live
// configuration against it with `nginx -t`, and swaps the container onto it.
type Images struct {
	eng Engine
	p   Paths
	cat *images.Catalog
	log *slog.Logger

	mu     sync.Mutex
	fams   map[string]*images.Family
	fam    *images.Family // the active family
	active string
}

// NewImages returns the nginx image family over the build catalogue.
func NewImages(eng Engine, p Paths, cat *images.Catalog, log *slog.Logger) (*Images, error) {
	i := &Images{eng: eng, p: p, cat: cat, log: log, fams: map[string]*images.Family{}, active: Version}
	// The version chosen last time, kept across restarts. A stored value that no
	// longer parses is ignored: the default family always exists.
	if v, ok, err := cat.ActiveVersion(context.Background(), images.KindNginx); err == nil && ok && CheckVersion(v) == nil {
		i.active = v
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
		Kind: images.KindNginx, Version: version, Repo: Repo, Catalog: i.cat, Engine: i.eng, Log: i.log,
		Hooks: images.Hooks{
			Build: func(ctx context.Context, b images.Build, progress docker.Progress) error {
				// Pull is set: an update is a new build from the newest base image
				// of the same tag, which is how nginx gets its security fixes.
				_, err := i.eng.ImageBuild(ctx, docker.BuildOptions{
					Tag: b.Tag, Dockerfile: DockerfileFor(version), Pull: true,
					Labels: map[string]string{docker.LabelManaged: "true"},
				}, progress)
				return err
			},
			Check: func(ctx context.Context, b images.Build, serving bool) (string, error) {
				// The first build is made while the panel is still writing nginx's
				// config files, so `nginx -t` would fail on files that do not exist
				// yet. Once nginx.conf is on disk the live configuration is checked
				// against the candidate, whether or not this family serves: that is
				// what makes a version switch safe.
				if !serving && !i.configPresent() {
					return "", nil
				}
				return checkCandidate(ctx, i.eng, i.p, b.Tag)
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

func (i *Images) configPresent() bool {
	_, err := os.Stat(i.p.ConfPath())
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

// ImageFunc is the image the container runs: the build that serves now.
func (i *Images) ImageFunc() ImageFunc {
	return func(ctx context.Context) (string, error) {
		tag, err := i.activeFamily().CurrentTag(ctx)
		if err != nil {
			return "", err
		}
		if tag == "" {
			return "", ErrNoImage
		}
		return tag, nil
	}
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

// Present reports whether a build serves and its image is on the host.
func (i *Images) Present(ctx context.Context) (bool, error) {
	tag, err := i.activeFamily().CurrentTag(ctx)
	if err != nil || tag == "" {
		return false, err
	}
	if _, err := i.eng.ImageInspect(ctx, tag); err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Build makes a new build from the newest base image, checks it with `nginx -t`
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
// and whose image is still here is reused; otherwise it is built from the
// official image. Either way the live configuration is checked against it before
// the container is touched, and a failed swap puts the old image back. The new
// version is recorded only once it serves.
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
		if _, ierr := i.eng.ImageInspect(ctx, cur.Tag); ierr == nil {
			reuse = true
		}
	}
	if reuse {
		// The family already points at this build, so Activate would not swap:
		// check and swap explicitly.
		if out, cerr := checkCandidate(ctx, i.eng, i.p, cur.Tag); cerr != nil {
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

// Reconcile is the boot step: it adopts the image from before builds had IDs and
// fails any build left running. It reports whether a build serves.
func (i *Images) Reconcile(ctx context.Context) (bool, error) {
	legacy := ""
	if i.Version() == Version {
		legacy = LegacyImage
	}
	return i.activeFamily().Reconcile(ctx, legacy)
}

// swap moves a running nginx container from one build to another. A container
// that is not running has nothing to move; the boot reconcile starts it on the
// build that serves. `from` is empty when the target belongs to a family that has
// not served yet (a version switch); the image serving now stands in for it, so a
// failed swap can put it back.
func (i *Images) swap(ctx context.Context, from, to string) error {
	c, err := i.eng.ContainerInspect(ctx, ContainerName)
	if errors.Is(err, docker.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !c.State.Running {
		return nil
	}
	if from == "" {
		from, _ = i.activeFamily().CurrentTag(ctx)
	}
	svc := NewService(i.eng, i.p, func(context.Context) (string, error) { return to, nil })
	return images.Swap(ctx, i.eng, images.SwapPlan{
		Name:    ContainerName,
		Mount:   i.p.Root,
		Spec:    func(image string) docker.Spec { return Spec(i.p, image) },
		Healthy: svc.Healthy,
	}, from, to)
}
