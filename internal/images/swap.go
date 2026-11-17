package images

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portbyte-id/server-panel/internal/docker"
)

// drain is how long the old container gets to finish its requests after the
// graceful stop signal (SIGQUIT for PHP-FPM and nginx) before it is killed.
const drain = 20 * time.Second

// SwapPlan describes one long-lived container whose image is replaced.
type SwapPlan struct {
	// Name is the container's name, the same before and after.
	Name string
	// Spec describes the container for an image reference.
	Spec func(image string) docker.Spec
	// Mount is the tree every mount must live under.
	Mount string
	// Healthy checks the container once it has started. An error here counts
	// as a failed start.
	Healthy func(ctx context.Context) error
}

// Swap moves the container from image `from` to image `to`.
//
// Both containers cannot run at once: they share sockets and ports (host
// network). So the old one is stopped gracefully, which lets it finish the
// requests it holds, and removed; the new one is then created from its spec and
// checked. If the new one does not start or fails its check, it is removed and
// `from` is started again. Requests arriving in the gap between the two get no
// answer; that gap is the cost of a swap, and the smoke test bounds it.
//
// `from` may be empty when no build serves yet. The caller only calls Swap for a
// running service.
func Swap(ctx context.Context, eng docker.Engine, p SwapPlan, from, to string) error {
	if err := retire(ctx, eng, p.Name); err != nil {
		return fmt.Errorf("stop the running container: %w", err)
	}
	startErr := start(ctx, eng, p, to)
	if startErr == nil {
		return nil
	}

	bg := context.WithoutCancel(ctx)
	restoreErr := retire(bg, eng, p.Name)
	state := "not restored"
	if from != "" {
		if err := start(bg, eng, p, from); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore %s: %w", from, err))
		} else {
			state = "running again"
		}
	}
	return errors.Join(fmt.Errorf("the new image did not come up (%s is %s): %w", to, state, startErr), restoreErr)
}

// retire stops and removes a container. A container that is not there is not
// an error: a swap may run after a container was lost.
func retire(ctx context.Context, eng docker.Engine, name string) error {
	if err := eng.ContainerStop(ctx, name, drain); err != nil && !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	if err := eng.ContainerRemove(ctx, name, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	return nil
}

// start creates the container for an image and checks it.
func start(ctx context.Context, eng docker.Engine, p SwapPlan, image string) error {
	if _, err := docker.Ensure(ctx, eng, p.Spec(image), p.Mount); err != nil {
		return err
	}
	return p.Healthy(ctx)
}
