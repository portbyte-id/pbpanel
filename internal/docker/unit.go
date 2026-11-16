package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"sort"
	"strings"
	"time"
)

// Labels the panel sets on every container it owns.
const (
	// LabelManaged marks a container as the panel's. Ensure refuses to replace
	// a container that lacks it, so a name clash with something the operator
	// started by hand is an error instead of a silent removal.
	LabelManaged = "panel.managed"
	// LabelRole says what the container is for: `nginx`, `php-8.3`.
	LabelRole = "panel.role"
	// LabelSpecHash is the hash of the Spec the container was created from.
	LabelSpecHash = "panel.spec-hash"

	// specHashVersion is mixed into the hash. Bump it when the way a Spec
	// becomes a container changes without the Spec changing, so existing
	// containers are recreated instead of being taken for current.
	specHashVersion = 1

	// stopTimeout is how long a container gets to exit before it is killed
	// during a recreate. nginx's graceful quit drains within seconds.
	stopTimeout = 15 * time.Second
)

// Mount is a bind mount whose path is the same on the host and in the
// container (plan.md §2), so a path in a config file means one thing.
type Mount struct {
	Path     string
	ReadOnly bool
}

// Spec describes a long-lived panel container. Networking and restart policy
// are not fields because they are not choices: every panel container uses the
// host network (decision 9) and restarts unless stopped.
type Spec struct {
	// Name is the container name, e.g. `panel-nginx`.
	Name  string
	Image string
	// Role is recorded in LabelRole.
	Role string
	// User is the user the main process starts as; empty keeps the image's.
	User       string
	Entrypoint []string
	Args       []string
	Env        []string
	Mounts     []Mount
	// Labels are extra labels. The panel.* labels above always win.
	Labels map[string]string
	// StopSignal overrides the image's stop signal when set.
	StopSignal string
}

// Validate checks a Spec. mountRoot is the tree every mount must live under,
// `/www` in production.
//
// That containment is the mount rule (risk 8): a bind mount hides whatever the
// image has at the same path, and an image keeps nothing under the panel's own
// root, so a mount that stays inside it cannot shadow the image's config. A
// container that needs one of the image's own directories reads it in place.
func (s Spec) Validate(mountRoot string) error {
	if err := checkName("container name", s.Name); err != nil {
		return err
	}
	if err := checkRef(s.Image); err != nil {
		return err
	}
	if strings.TrimSpace(s.Role) == "" {
		return errors.New("docker: spec: role is required")
	}
	if !path.IsAbs(mountRoot) || path.Clean(mountRoot) != mountRoot || mountRoot == "/" {
		return fmt.Errorf("docker: spec: mount root %q must be a clean absolute path below /", mountRoot)
	}
	seen := map[string]bool{}
	for _, m := range s.Mounts {
		switch {
		case !path.IsAbs(m.Path) || path.Clean(m.Path) != m.Path:
			return fmt.Errorf("docker: spec: mount %q must be a clean absolute path", m.Path)
		case strings.ContainsAny(m.Path, ":\x00"):
			// A colon is the separator of the bind syntax.
			return fmt.Errorf("docker: spec: mount %q contains a character a bind mount cannot carry", m.Path)
		case !strings.HasPrefix(m.Path, mountRoot+"/"):
			return fmt.Errorf("docker: spec: mount %q is outside %s: mounting there could hide a directory the image needs", m.Path, mountRoot)
		case seen[m.Path]:
			return fmt.Errorf("docker: spec: mount %q is listed twice", m.Path)
		}
		seen[m.Path] = true
	}
	return nil
}

// Hash is a stable digest of everything that decides what the container is.
// Two specs with the same hash produce the same container, so a container
// carrying the hash needs no recreate. Order that carries no meaning (mounts,
// labels) does not affect it; order that does (arguments, environment) does.
func (s Spec) Hash() string {
	mounts := append([]Mount(nil), s.Mounts...)
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Path < mounts[j].Path })
	canonical := struct {
		V          int
		Name       string
		Image      string
		Role       string
		User       string
		Entrypoint []string
		Args       []string
		Env        []string
		Mounts     []Mount
		Labels     map[string]string // encoding/json writes map keys sorted
		StopSignal string
	}{specHashVersion, s.Name, s.Image, s.Role, s.User, s.Entrypoint, s.Args, s.Env, mounts, s.Labels, s.StopSignal}
	raw, err := json.Marshal(canonical)
	if err != nil {
		// Strings, slices of strings and a string map cannot fail to marshal.
		panic("docker: spec hash: " + err.Error())
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Config is the container the Spec describes.
func (s Spec) Config() ContainerConfig {
	labels := make(map[string]string, len(s.Labels)+3)
	maps.Copy(labels, s.Labels)
	labels[LabelManaged] = "true"
	labels[LabelRole] = s.Role
	labels[LabelSpecHash] = s.Hash()

	binds := make([]string, 0, len(s.Mounts))
	for _, m := range s.Mounts {
		b := m.Path + ":" + m.Path
		if m.ReadOnly {
			b += ":ro"
		}
		binds = append(binds, b)
	}
	return ContainerConfig{
		Image: s.Image, User: s.User, Entrypoint: s.Entrypoint, Cmd: s.Args, Env: s.Env,
		Labels: labels, StopSignal: s.StopSignal, Binds: binds,
		NetworkMode: "host", RestartPolicy: "unless-stopped",
	}
}

// Action is what Ensure has to do to make a container match its Spec.
type Action string

// Ensure actions.
const (
	ActionNone     Action = "none"
	ActionCreate   Action = "create"
	ActionRecreate Action = "recreate"
	ActionStart    Action = "start"
)

// ErrForeignContainer is returned when the name a Spec wants belongs to a
// container the panel did not create.
var ErrForeignContainer = errors.New("docker: a container with this name exists and is not managed by the panel")

// Decide compares a Spec with the container currently holding its name (nil
// when there is none) and the ID its image reference resolves to now.
func Decide(spec Spec, imageID string, current *Container) (Action, string, error) {
	if current == nil {
		return ActionCreate, "no container named " + spec.Name, nil
	}
	if current.Labels[LabelManaged] != "true" {
		return "", "", fmt.Errorf("%w: %s", ErrForeignContainer, spec.Name)
	}
	if current.Labels[LabelSpecHash] != spec.Hash() {
		return ActionRecreate, "the container was created from a different spec", nil
	}
	// The same tag can point at a new image after a rebuild or a pull; the
	// spec hash cannot see that, the image ID can.
	if imageID != "" && current.ImageID != imageID {
		return ActionRecreate, "the image behind " + spec.Image + " has changed", nil
	}
	if !current.State.Running {
		return ActionStart, "the container is " + stateWord(current.State), nil
	}
	return ActionNone, "the container is running and matches its spec", nil
}

func stateWord(s ContainerState) string {
	if s.Status != "" {
		return s.Status
	}
	return "not running"
}

// Engine is the part of the client Ensure needs. It exists so the decisions
// are tested against a fake that records calls.
type Engine interface {
	ImageInspect(ctx context.Context, ref string) (Image, error)
	ContainerInspect(ctx context.Context, id string) (Container, error)
	ContainerCreate(ctx context.Context, name string, cfg ContainerConfig) (string, error)
	ContainerStart(ctx context.Context, id string) error
	ContainerStop(ctx context.Context, id string, timeout time.Duration) error
	ContainerRemove(ctx context.Context, id string, force bool) error
}

// EnsureResult reports what Ensure did.
type EnsureResult struct {
	Action Action
	Reason string
	// ID is the container's ID after Ensure.
	ID string
}

// Ensure makes the container described by spec exist and run. It is
// idempotent: create if missing, recreate if the spec or the image changed,
// start if stopped, nothing otherwise.
//
// The image must already be present; pulling or building it is a job with
// visible progress, not a side effect of a reconcile.
func Ensure(ctx context.Context, eng Engine, spec Spec, mountRoot string) (EnsureResult, error) {
	if err := spec.Validate(mountRoot); err != nil {
		return EnsureResult{}, err
	}
	img, err := eng.ImageInspect(ctx, spec.Image)
	if err != nil {
		return EnsureResult{}, fmt.Errorf("docker: ensure %s: image %s: %w", spec.Name, spec.Image, err)
	}

	var current *Container
	switch c, err := eng.ContainerInspect(ctx, spec.Name); {
	case err == nil:
		current = &c
	case !errors.Is(err, ErrNotFound):
		return EnsureResult{}, fmt.Errorf("docker: ensure %s: %w", spec.Name, err)
	}

	action, reason, err := Decide(spec, img.ID, current)
	if err != nil {
		return EnsureResult{}, err
	}
	res := EnsureResult{Action: action, Reason: reason}
	if current != nil {
		res.ID = current.ID
	}

	switch action {
	case ActionNone:
		return res, nil
	case ActionStart:
		if err := eng.ContainerStart(ctx, current.ID); err != nil {
			return res, fmt.Errorf("docker: ensure %s: %w", spec.Name, err)
		}
		return res, nil
	case ActionRecreate:
		if err := eng.ContainerStop(ctx, current.ID, stopTimeout); err != nil && !errors.Is(err, ErrNotFound) {
			return res, fmt.Errorf("docker: ensure %s: %w", spec.Name, err)
		}
		if err := eng.ContainerRemove(ctx, current.ID, true); err != nil && !errors.Is(err, ErrNotFound) {
			return res, fmt.Errorf("docker: ensure %s: %w", spec.Name, err)
		}
	}

	id, err := eng.ContainerCreate(ctx, spec.Name, spec.Config())
	if err != nil {
		return res, fmt.Errorf("docker: ensure %s: %w", spec.Name, err)
	}
	res.ID = id
	if err := eng.ContainerStart(ctx, id); err != nil {
		return res, fmt.Errorf("docker: ensure %s: %w", spec.Name, err)
	}
	return res, nil
}
