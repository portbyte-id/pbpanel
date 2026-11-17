//go:build linux

package fm

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// OpenDownload opens a regular file for reading. A final symlink is followed
// (the operator clicked on it, and root may read everything by design), but a
// FIFO, socket or device is refused: opening one could block the request, and
// streaming a device is not a download.
func (s *Service) OpenDownload(raw string) (*os.File, os.FileInfo, error) {
	clean, err := s.pol.Clean(raw)
	if err != nil {
		return nil, nil, err
	}
	fd, err := unix.Open(clean, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	f := os.NewFile(uintptr(fd), clean)
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	switch {
	case info.IsDir():
		_ = f.Close()
		return nil, nil, ErrIsDir
	case !info.Mode().IsRegular():
		_ = f.Close()
		return nil, nil, fmt.Errorf("%w: not a regular file", ErrUnsupported)
	}
	return f, info, nil
}
