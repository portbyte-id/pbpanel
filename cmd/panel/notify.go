package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/notify"
	"github.com/portbyte-id/server-panel/internal/secretbox"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/store"
)

// newNotify builds the notification engine and starts its dispatcher, which
// stops when ctx ends. A host without a usable secret key still gets the
// engine and the bell; only storing a channel is refused (D2).
func newNotify(ctx context.Context, st *store.Store, reg *settings.Registry, layout install.Layout, logger *slog.Logger) (*notify.Service, *notify.Admin) {
	var box *secretbox.Box
	if key, err := os.ReadFile(layout.SecretKeyPath()); err != nil {
		logger.Warn("notify: no secret key; channels cannot be stored", "error", err)
	} else if b, err := secretbox.New(key); err != nil {
		logger.Warn("notify: secret key unusable; channels cannot be stored", "error", err)
	} else {
		box = b
	}

	repo := notify.NewRepo(st, box, nil)
	svc := notify.New(notify.Options{Repo: repo, Settings: reg, Logger: logger})
	disp := notify.NewDispatcher(notify.DispatcherOptions{
		Service:  svc,
		Host:     notifyHost(ctx, reg),
		PanelURL: notifyPanelURL(ctx, reg),
		Logger:   logger,
	})
	go disp.Run(ctx)
	return svc, notify.NewAdmin(svc, disp)
}

// notifyHost is the name in messages: notify.label, else the machine's host name.
func notifyHost(ctx context.Context, reg *settings.Registry) string {
	if v, err := reg.Str(ctx, settings.KeyNotifyLabel); err == nil && v != "" {
		return v
	}
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "panel"
}

// notifyPanelURL is the absolute base for links: notify.panel_url, else the
// panel domain. Empty means messages carry no link.
func notifyPanelURL(ctx context.Context, reg *settings.Registry) string {
	if v, err := reg.Str(ctx, settings.KeyNotifyPanelURL); err == nil && v != "" {
		return v
	}
	domain, err := reg.Str(ctx, settings.KeyPanelDomain)
	if err != nil || domain == "" {
		return ""
	}
	port, _ := reg.Str(ctx, "panel.port")
	if port == "" || port == "443" {
		return "https://" + domain
	}
	return "https://" + domain + ":" + port
}
