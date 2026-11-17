//go:build !linux

package main

import (
	"os"

	"github.com/portbyte-id/server-panel/internal/apps"
)

// prepareAppDir makes an app's working directory. Ownership is a property of a
// Linux host, so there is nothing to hand over here.
func prepareAppDir(a apps.App) error { return os.MkdirAll(a.WorkDir, 0o755) }
