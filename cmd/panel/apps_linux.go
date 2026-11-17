//go:build linux

package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"

	"github.com/portbyte-id/server-panel/internal/apps"
)

// prepareAppDir makes an app's working directory and hands a new one to the
// account the app runs as. The account must exist: supervisord would otherwise
// fail to start the program, and the operator would find out from a log instead
// of from the form. A directory that already exists is the operator's and keeps
// its owner.
func prepareAppDir(a apps.App) error {
	u, err := user.Lookup(a.User)
	if err != nil {
		return fmt.Errorf("the account %q does not exist on this host", a.User)
	}
	_, statErr := os.Stat(a.WorkDir)
	existed := statErr == nil
	if err := os.MkdirAll(a.WorkDir, 0o755); err != nil {
		return err
	}
	if existed {
		return nil
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}
	return os.Chown(a.WorkDir, uid, gid)
}
