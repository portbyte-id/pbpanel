//go:build linux

package main

import (
	"context"
	"log/slog"

	"github.com/portbyte-id/server-panel/internal/fm"
	"github.com/portbyte-id/server-panel/internal/httpapi"
	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/store"
)

// newFileManager builds the file manager over the panel tree. Its limits are
// read from the settings registry on every operation, so a change on the
// Settings page applies at once. A failure leaves the /api/fm routes at 503.
func newFileManager(ctx context.Context, st *store.Store, reg *settings.Registry, layout install.Layout, logger *slog.Logger) httpapi.FileManager {
	svc, err := fm.New(fm.Options{
		Store:    st,
		PanelDir: layout.PanelDir(),
		Limits: func(c context.Context) fm.Limits {
			d := fm.DefaultLimits()
			get := func(key string, into *int64) {
				if v, err := reg.Int(c, key); err == nil {
					*into = int64(v)
				}
			}
			get(settings.KeyFMEditorMaxBytes, &d.EditorMaxBytes)
			get(settings.KeyFMChunkMaxBytes, &d.ChunkMaxBytes)
			get(settings.KeyFMTrashMaxBytes, &d.TrashMaxBytes)
			get(settings.KeyFMArchiveMaxBytes, &d.ArchiveMaxBytes)
			if v, err := reg.Int(c, settings.KeyFMTrashRetentionDays); err == nil {
				d.TrashRetentionDays = v
			}
			if v, err := reg.Int(c, settings.KeyFMArchiveMaxEntries); err == nil {
				d.ArchiveMaxEntries = v
			}
			if v, err := reg.Duration(c, settings.KeyFMUploadSweepAge); err == nil {
				d.UploadSweepAge = v
			}
			return d
		},
	})
	if err != nil {
		logger.Error("file manager is not available", "error", err)
		return nil
	}
	go svc.RunPurgeLoop(ctx, func(err error) { logger.Warn("file manager housekeeping", "error", err) })
	return svc
}
