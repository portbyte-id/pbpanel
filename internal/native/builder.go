package native

import (
	"context"
	"io"

	"github.com/portbyte-id/server-panel/internal/docker"
)

// BuildEngine is the part of the Docker client the builder uses. Docker only
// compiles: nothing it makes keeps running.
type BuildEngine interface {
	ImageBuild(ctx context.Context, opts docker.BuildOptions, progress docker.Progress) (docker.Image, error)
	ContainerCreate(ctx context.Context, name string, cfg docker.ContainerConfig) (string, error)
	ContainerArchive(ctx context.Context, id, path string) (io.ReadCloser, error)
	ContainerRemove(ctx context.Context, id string, force bool) error
	ImageRemove(ctx context.Context, ref string) error
}

// Spec says what one product's build is: the recipe, where its prefix lives, what
// a finished build must hold, and how its throwaway image and container are named.
// Product is what progress lines and errors call it.
type Spec struct {
	Product string
	// TagPrefix names the build image: <TagPrefix>:<id>.
	TagPrefix string
	// ContainerPrefix names the builder container: <ContainerPrefix><id>.
	ContainerPrefix string
	// BuildDir is the build's prefix inside the builder image, which is also the
	// path read back out of it.
	BuildDir func(id string) string
	// VersionsDir is where finished builds are kept on the host.
	VersionsDir func() string
	// Dockerfile produces the recipe for one build on one builder image.
	Dockerfile func(base, id, version string) []byte
	// Required lists the paths a finished build must hold: the program itself and
	// whatever records what it compiled.
	Required []string
}

// DockerBuilder compiles a build in a builder image and reads the prefix out of it.
type DockerBuilder struct {
	Eng  BuildEngine
	Spec Spec
	// OSRelease is the file the builder image is matched on; empty means
	// /etc/os-release.
	OSRelease string
}

// Build compiles build id of version and leaves it at Spec.VersionsDir()/id. The
// builder image and container are removed whether or not it worked.
func (b DockerBuilder) Build(ctx context.Context, id, version string, progress docker.Progress) error {
	file := b.OSRelease
	if file == "" {
		file = "/etc/os-release"
	}
	host, err := ReadHost(file, b.Spec.Product)
	if err != nil {
		return err
	}
	base, err := BuilderImage(host, b.Spec.Product)
	if err != nil {
		return err
	}
	say(progress, "building "+b.Spec.Product+" "+version+" on "+base)

	tag := b.Spec.TagPrefix + ":" + id
	bg := context.WithoutCancel(ctx)
	if _, err := b.Eng.ImageBuild(ctx, docker.BuildOptions{
		Tag: tag, Dockerfile: b.Spec.Dockerfile(base, id, version), Pull: true,
		Labels: map[string]string{docker.LabelManaged: "true"},
	}, progress); err != nil {
		return err
	}
	defer func() { _ = b.Eng.ImageRemove(bg, tag) }()

	name := b.Spec.ContainerPrefix + id
	cid, err := b.Eng.ContainerCreate(ctx, name, docker.ContainerConfig{
		Image: tag, Cmd: []string{"true"},
		Labels: map[string]string{docker.LabelManaged: "true"},
	})
	if err != nil {
		return err
	}
	defer func() { _ = b.Eng.ContainerRemove(bg, cid, true) }()

	say(progress, "copying the build out of the builder image")
	rc, err := b.Eng.ContainerArchive(ctx, cid, b.Spec.BuildDir(id))
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	return ExtractBuild(rc, b.Spec.VersionsDir(), id, b.Spec.Product, b.Spec.Required)
}

func say(p docker.Progress, line string) {
	if p != nil {
		p(line)
	}
}
