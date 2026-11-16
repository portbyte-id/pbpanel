//go:build !unix

package confedit

import (
	"io/fs"
	"os"
)

// The daemon runs on Linux only. These stand-ins let the package build and its
// platform-independent tests run on a developer machine; ownership and
// directory syncs are covered by the unix tests.

func ownerOf(fs.FileInfo) owner { return owner{} }

func chownFile(*os.File, owner) error { return nil }

func syncDir(string) error { return nil }
