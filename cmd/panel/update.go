package main

import (
	"os"

	"github.com/portbyte-id/server-panel/internal/httpapi"
	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/osdep"
	"github.com/portbyte-id/server-panel/internal/update"
	"github.com/portbyte-id/server-panel/internal/version"
)

// newUpdate builds the version check and the detached upgrade the daemon serves.
// The repository and token are read the same way `pbp upgrade` reads them, so
// the footer checks the release the CLI would install.
func newUpdate(layout install.Layout) httpapi.UpdateService {
	token := os.Getenv("PB_GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	return &update.Service{
		Check: &update.Checker{
			Current: version.Version,
			Repo:    os.Getenv("PB_REPO"),
			Token:   token,
		},
		Upgrade: &update.Starter{
			BinaryPath: layout.BinaryPath(),
			StatusFile: update.StatusPath(layout.PanelDir()),
			Runner:     osdep.DefaultRunner(),
		},
	}
}
