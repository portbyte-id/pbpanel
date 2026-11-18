package nginx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
)

// idLen is the length of a build ID (images.NewID): 14 digits, a dash, 6 hex.
const idLen = 21

// idOfTag is the build ID a catalogue reference ends in: `panel/nginx:1.30-<id>`.
func idOfTag(tag string) (string, error) {
	if len(tag) < idLen {
		return "", fmt.Errorf("nginx: %q is not a build reference", tag)
	}
	id := tag[len(tag)-idLen:]
	if err := images.CheckID(id); err != nil {
		return "", err
	}
	return id, nil
}

// dirStore is images.Engine over directories: a build "exists" while its
// directory holds a binary. It is what lets the shared catalogue, which was
// written for Docker images, keep nginx's builds without knowing they are no
// longer images.
type dirStore struct{ p Paths }

// ImageInspect reports the build a reference names, or an error matching
// docker.ErrNotFound when its files are gone.
func (s dirStore) ImageInspect(_ context.Context, ref string) (docker.Image, error) {
	id, err := idOfTag(ref)
	if err != nil {
		return docker.Image{}, err
	}
	if _, err := os.Stat(filepath.FromSlash(s.p.BinaryOf(id))); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return docker.Image{}, fmt.Errorf("nginx build %s is not on disk: %w", id, docker.ErrNotFound)
		}
		return docker.Image{}, err
	}
	return docker.Image{ID: id}, nil
}

// ImageTag does nothing: a build has one name.
func (dirStore) ImageTag(context.Context, string, string) error { return nil }

// ImageRemove deletes a build's directory. The build the link names is never
// removed; the catalogue only prunes builds that are neither current nor previous,
// and this is the second lock on that.
func (s dirStore) ImageRemove(_ context.Context, ref string) error {
	id, err := idOfTag(ref)
	if err != nil {
		return err
	}
	if cur, _ := readLink(s.p); cur == id {
		return fmt.Errorf("nginx: build %s is the one that serves and is not removed", id)
	}
	return os.RemoveAll(filepath.FromSlash(s.p.BuildDir(id)))
}

// readLink is the ID of the build the CurrentLink names. An absent link is an
// error matching os.ErrNotExist.
func readLink(p Paths) (string, error) {
	target, err := os.Readlink(filepath.FromSlash(p.CurrentLink()))
	if err != nil {
		return "", err
	}
	id := filepath.Base(filepath.ToSlash(target))
	if err := images.CheckID(id); err != nil {
		return "", err
	}
	return id, nil
}

// setLink points the CurrentLink at a build. The new link is made beside the old
// one and renamed over it, so there is no moment when the path does not resolve.
// The target is relative, so the whole tree can be moved.
func setLink(p Paths, id string) error {
	if err := images.CheckID(id); err != nil {
		return err
	}
	link := filepath.FromSlash(p.CurrentLink())
	tmp := link + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join("versions", id), tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("nginx: point %s at build %s: %w", link, id, err)
	}
	return nil
}
