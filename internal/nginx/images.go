package nginx

import (
	"context"
	"errors"
	"log/slog"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
)

// Images owns the nginx image builds (development plan M8, steps 8.1 and 8.6):
// each build of the image is kept under its own tag, one build serves, and the
// build served before is kept so one action can put it back.
type Images struct {
	fam *images.Family
	eng Engine
	p   Paths
}

// NewImages returns the nginx image family over the build catalogue.
func NewImages(eng Engine, p Paths, cat *images.Catalog, log *slog.Logger) (*Images, error) {
	i := &Images{eng: eng, p: p}
	fam, err := images.NewFamily(images.FamilyOptions{
		Kind: images.KindNginx, Version: Version, Repo: Repo, Catalog: cat, Engine: eng, Log: log,
		Hooks: images.Hooks{
			Build: func(ctx context.Context, b images.Build, progress docker.Progress) error {
				// Pull is set: an update is a new build from the newest base image
				// of the same tag, which is how nginx gets its security fixes.
				_, err := eng.ImageBuild(ctx, docker.BuildOptions{
					Tag: b.Tag, Dockerfile: Dockerfile(), Pull: true,
					Labels: map[string]string{docker.LabelManaged: "true"},
				}, progress)
				return err
			},
			Check: func(ctx context.Context, b images.Build, serving bool) (string, error) {
				// The first build is made while the panel is still writing nginx's
				// include files, so `nginx -t` would fail on files that do not exist
				// yet. The apply that writes them validates the configuration with
				// the image that serves at that point.
				if !serving {
					return "", nil
				}
				return checkCandidate(ctx, eng, p, b.Tag)
			},
			Swap: i.swap,
		},
	})
	if err != nil {
		return nil, err
	}
	i.fam = fam
	return i, nil
}

// ImageFunc is the image the container runs: the build that serves now.
func (i *Images) ImageFunc() ImageFunc {
	return func(ctx context.Context) (string, error) {
		tag, err := i.fam.CurrentTag(ctx)
		if err != nil {
			return "", err
		}
		if tag == "" {
			return "", ErrNoImage
		}
		return tag, nil
	}
}

// Idle reports images.ErrBusy while a build, update or rollback is running.
func (i *Images) Idle() error { return i.fam.Idle() }

// Builds lists the builds, newest first, flagged with what serves and what
// served before.
func (i *Images) Builds(ctx context.Context) ([]images.Build, error) {
	return i.fam.Builds(ctx)
}

// Current returns the build that serves, and false when none does.
func (i *Images) Current(ctx context.Context) (images.Build, bool, error) {
	return i.fam.Current(ctx)
}

// Present reports whether a build serves and its image is on the host.
func (i *Images) Present(ctx context.Context) (bool, error) {
	tag, err := i.fam.CurrentTag(ctx)
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
	return i.fam.Build(ctx, nil, progress)
}

// Activate makes an earlier ready build serve: the rollback.
func (i *Images) Activate(ctx context.Context, id string) (images.Build, error) {
	return i.fam.Activate(ctx, id)
}

// Reconcile is the boot step: it adopts the image from before builds had IDs and
// fails any build left running. It reports whether a build serves.
func (i *Images) Reconcile(ctx context.Context) (bool, error) {
	return i.fam.Reconcile(ctx, LegacyImage)
}

// swap moves a running nginx container from one build to another. A container
// that is not running has nothing to move; the boot reconcile starts it on the
// build that serves.
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
	svc := NewService(i.eng, i.p, func(context.Context) (string, error) { return to, nil })
	return images.Swap(ctx, i.eng, images.SwapPlan{
		Name:    ContainerName,
		Mount:   i.p.Root,
		Spec:    func(image string) docker.Spec { return Spec(i.p, image) },
		Healthy: svc.Healthy,
	}, from, to)
}
