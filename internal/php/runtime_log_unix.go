//go:build unix

package php

import (
	"errors"
	"io/fs"
	"os"
)

// createLogFile makes FPM's own log the unit is about to open. It is created only
// when absent: an existing log keeps its content and its owner, so a restart does
// not throw away what the master reported.
//
// www owns it because the master runs as root and opens it, then a pool that drops
// to www may still be asked to write there; 0640 keeps it off other accounts.
func createLogFile(file string) error {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// The panel daemon runs as root; a test runs as whoever started it and cannot
	// give the file away. There is no www account to give it to then either.
	if os.Geteuid() != 0 {
		return nil
	}
	return os.Chown(file, WWWUID, WWWGID)
}
