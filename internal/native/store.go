package native

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

// IDOfTag is the build ID a catalogue reference ends in: `panel/nginx:1.30-<id>`.
func IDOfTag(tag, product string) (string, error) {
	if len(tag) < idLen {
		return "", fmt.Errorf("%s: %q is not a build reference", product, tag)
	}
	id := tag[len(tag)-idLen:]
	if err := images.CheckID(id); err != nil {
		return "", err
	}
	return id, nil
}

// Store is images.Engine over directories: a build "exists" while its directory
// holds a binary. It is what lets the shared catalogue, which was written for
// Docker images, keep a product's builds without knowing they are no longer
// images.
type Store struct {
	// VersionsDir holds one directory per build.
	VersionsDir string
	// BinaryOf is one build's executable: the file whose presence says the build
	// is there.
	BinaryOf func(id string) string
	// Link is the symlink that names the build that serves.
	Link string
	// Product is what errors call this thing.
	Product string
}

// ImageInspect reports the build a reference names, or an error matching
// docker.ErrNotFound when its files are gone.
func (s Store) ImageInspect(_ context.Context, ref string) (docker.Image, error) {
	id, err := IDOfTag(ref, s.Product)
	if err != nil {
		return docker.Image{}, err
	}
	if _, err := os.Stat(filepath.FromSlash(s.BinaryOf(id))); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return docker.Image{}, fmt.Errorf("%s build %s is not on disk: %w", s.Product, id, docker.ErrNotFound)
		}
		return docker.Image{}, err
	}
	return docker.Image{ID: id}, nil
}

// ImageRemove deletes a build's directory. The build the link names is never
// removed; the catalogue only prunes builds that are neither current nor previous,
// and this is the second lock on that.
func (s Store) ImageRemove(_ context.Context, ref string) error {
	id, err := IDOfTag(ref, s.Product)
	if err != nil {
		return err
	}
	if cur, _ := ReadLink(s.Link); cur == id {
		return fmt.Errorf("%s: build %s is the one that serves and is not removed", s.Product, id)
	}
	return os.RemoveAll(filepath.Join(filepath.FromSlash(s.VersionsDir), id))
}

// ReadLink is the ID of the build the link names. An absent link is an error
// matching os.ErrNotExist.
func ReadLink(link string) (string, error) {
	target, err := os.Readlink(filepath.FromSlash(link))
	if err != nil {
		return "", err
	}
	id := filepath.Base(filepath.ToSlash(target))
	if err := images.CheckID(id); err != nil {
		return "", err
	}
	return id, nil
}

// SetLink points the link at a build. The new link is made beside the old one and
// renamed over it, so there is no moment when the path does not resolve. The
// target is relative, so the whole tree can be moved.
func SetLink(link, id, product string) error {
	if err := images.CheckID(id); err != nil {
		return err
	}
	l := filepath.FromSlash(link)
	tmp := l + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join("versions", id), tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, l); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%s: point %s at build %s: %w", product, l, id, err)
	}
	return nil
}
