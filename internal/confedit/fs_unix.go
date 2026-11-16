//go:build unix

package confedit

import (
	"io/fs"
	"os"
	"syscall"
)

func ownerOf(info fs.FileInfo) owner {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return owner{}
	}
	return owner{uid: int(st.Uid), gid: int(st.Gid), valid: true}
}

func chownFile(f *os.File, own owner) error {
	if !own.valid {
		return nil
	}
	return f.Chown(own.uid, own.gid)
}

// syncDir flushes a directory entry change (a rename or a create) to disk.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
