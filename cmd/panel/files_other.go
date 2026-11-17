//go:build !linux

package main

import (
	"context"
	"log/slog"

	"github.com/portbyte-id/server-panel/internal/httpapi"
	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/store"
)

// newFileManager: the file manager needs Linux descriptors and /proc, so on any
// other host the /api/fm routes answer 503.
func newFileManager(context.Context, *store.Store, *settings.Registry, install.Layout, *slog.Logger) httpapi.FileManager {
	return nil
}
