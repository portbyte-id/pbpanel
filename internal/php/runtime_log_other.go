//go:build !unix

package php

import (
	"errors"
	"io/fs"
	"os"
)

// createLogFile makes FPM's own log. Off Unix nothing is chowned: this package
// only runs on Linux in production, and the build tag exists so the tests build
// on a development machine.
func createLogFile(file string) error {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return f.Close()
}
