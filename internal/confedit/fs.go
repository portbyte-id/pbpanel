package confedit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// owner is a file's numeric owner. It is carried through a rewrite so a file
// that belonged to `www` still does afterwards; valid is false where the
// platform has no such notion.
type owner struct {
	uid, gid int
	valid    bool
}

// snapshot is one file as it was before an apply touched it.
type snapshot struct {
	path    string
	existed bool
	content []byte
	mode    fs.FileMode
	owner   owner
}

// takeSnapshot reads the current state of path. A file that does not exist is a
// valid state: restoring it means removing whatever the apply created.
func takeSnapshot(path string, maxBytes int64) (snapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return snapshot{path: path}, nil
	}
	if err != nil {
		return snapshot{}, err
	}
	// Lstat, not Stat: a managed config that has become a symlink would have
	// the rename replace the link while the validator reads the target.
	if !info.Mode().IsRegular() {
		return snapshot{}, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > maxBytes {
		return snapshot{}, fmt.Errorf("%s is %d bytes, above the %d byte limit for a managed config", path, info.Size(), maxBytes)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return snapshot{}, err
	}
	return snapshot{path: path, existed: true, content: content, mode: info.Mode().Perm(), owner: ownerOf(info)}, nil
}

// restore puts the file back exactly as the snapshot found it.
func (s snapshot) restore(write writeFunc) error {
	if !s.existed {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return write(s.path, s.content, s.mode, s.owner)
}

// writeFunc is the signature of writeAtomic. The editor calls it through a
// field so a test can make the Nth write fail.
type writeFunc func(path string, data []byte, mode fs.FileMode, own owner) error

// writeAtomic replaces path with data so that a reader, or a crash, sees either
// the old file or the new one and never a partial write: the data goes to a
// temporary file in the same directory, is synced, and is renamed over the
// target. The directory is synced too, or the rename itself could be lost.
func writeAtomic(path string, data []byte, mode fs.FileMode, own owner) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	// Mode and owner are set on the temporary file, before the rename, so the
	// target never exists with the wrong ones.
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if err = chownFile(tmp, own); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	return syncDir(dir)
}
