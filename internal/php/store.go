package php

import (
	"context"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/native"
)

// dirStore is images.Engine over one version's build directories: a build
// "exists" while its directory holds an FPM master. It is what lets the shared
// catalogue, which was written for Docker images, keep a version's builds
// without knowing they are no longer images.
type dirStore struct {
	p   Paths
	ver string
}

func (s dirStore) store() native.Store {
	return native.Store{
		VersionsDir: s.p.VersionsDir(s.ver),
		BinaryOf:    func(id string) string { return s.p.BinaryOf(s.ver, id) },
		Link:        s.p.CurrentLink(s.ver),
		Product:     product,
	}
}

// ImageInspect reports the build a reference names, or an error matching
// docker.ErrNotFound when its files are gone.
func (s dirStore) ImageInspect(ctx context.Context, ref string) (docker.Image, error) {
	return s.store().ImageInspect(ctx, ref)
}

// ImageTag does nothing: a build has one name.
func (s dirStore) ImageTag(ctx context.Context, from, to string) error {
	return s.store().ImageTag(ctx, from, to)
}

// ImageRemove deletes a build's directory. The build the link names is never
// removed; the catalogue only prunes builds that are neither current nor
// previous, and this is the second lock on that.
func (s dirStore) ImageRemove(ctx context.Context, ref string) error {
	return s.store().ImageRemove(ctx, ref)
}

// readLink is the ID of the build a version's current link names.
func readLink(p Paths, ver string) (string, error) { return native.ReadLink(p.CurrentLink(ver)) }

// setLink points a version's current link at a build.
func setLink(p Paths, ver, id string) error { return native.SetLink(p.CurrentLink(ver), id, product) }
