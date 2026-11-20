package nginx

import (
	"context"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/native"
)

// idOfTag is the build ID a catalogue reference ends in: `panel/nginx:1.30-<id>`.
func idOfTag(tag string) (string, error) { return native.IDOfTag(tag, product) }

// dirStore is images.Engine over directories: a build "exists" while its
// directory holds a binary. It is what lets the shared catalogue, which was
// written for Docker images, keep nginx's builds without knowing they are no
// longer images.
type dirStore struct{ p Paths }

func (s dirStore) store() native.Store {
	return native.Store{
		VersionsDir: s.p.VersionsDir(),
		BinaryOf:    s.p.BinaryOf,
		Link:        s.p.CurrentLink(),
		Product:     product,
	}
}

// ImageInspect reports the build a reference names, or an error matching
// docker.ErrNotFound when its files are gone.
func (s dirStore) ImageInspect(ctx context.Context, ref string) (docker.Image, error) {
	return s.store().ImageInspect(ctx, ref)
}

// ImageRemove deletes a build's directory. The build the link names is never
// removed; the catalogue only prunes builds that are neither current nor previous,
// and this is the second lock on that.
func (s dirStore) ImageRemove(ctx context.Context, ref string) error {
	return s.store().ImageRemove(ctx, ref)
}

// readLink is the ID of the build the CurrentLink names.
func readLink(p Paths) (string, error) { return native.ReadLink(p.CurrentLink()) }

// setLink points the CurrentLink at a build.
func setLink(p Paths, id string) error { return native.SetLink(p.CurrentLink(), id, product) }
