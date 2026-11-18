package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"runtime"

	"github.com/portbyte-id/server-panel/internal/firewall"
	"github.com/portbyte-id/server-panel/internal/httpapi"
	"github.com/portbyte-id/server-panel/internal/osdep"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/store"
)

// newFirewall builds the firewall service and rolls back an apply that a restart
// left unconfirmed. A host without iptables, or any wiring failure, leaves the
// /api/firewall routes at 503; the panel itself still starts.
func newFirewall(ctx context.Context, st *store.Store, reg *settings.Registry, logger *slog.Logger) httpapi.FirewallService {
	if runtime.GOOS != "linux" {
		return nil
	}
	if _, err := exec.LookPath("iptables-restore"); err != nil {
		logger.Warn("firewall is not available: iptables-restore not found", "error", err)
		return nil
	}
	cmd := firewall.ExecCmd{}
	mgr, err := firewall.NewManager(firewall.Options{
		Store: st, Cmd: cmd, IPv6: firewall.HostHasIPv6(),
		OnError: func(err error) { logger.Error("firewall", "error", err) },
	})
	if err != nil {
		logger.Error("firewall is not available", "error", err)
		return nil
	}
	// Before serving: an apply the operator never kept must not outlive the daemon.
	if err := mgr.Recover(ctx); err != nil {
		logger.Error("firewall: rolling back the unconfirmed apply failed", "error", err)
	}
	svc, err := firewall.NewService(firewall.ServiceOptions{
		Manager: mgr, Store: st, Cmd: cmd,
		Masker:    osdep.NewUbuntu(osdep.DefaultRunner()),
		PanelPort: func() int { return intSetting(ctx, reg, "panel.port", 0) },
	})
	if err != nil {
		logger.Error("firewall is not available", "error", err)
		return nil
	}
	return svc
}

// openPanelPort keeps the panel port reachable through the host firewall, outside the
// managed ruleset. It runs at install, at every daemon start and at boot, so the port
// stays open whether or not the operator ever enables the firewall.
func openPanelPort(ctx context.Context, port int) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	v6 := firewall.HostHasIPv6()
	return errors.Join(
		firewall.OpenPanelPort(ctx, firewall.ExecCmd{}, port, v6),
		firewall.OpenWebPorts(ctx, firewall.ExecCmd{}, v6),
	)
}

// firewallRestore replays the last confirmed ruleset and re-opens the panel port.
// panel-firewall.service runs it at boot, before the daemon, so it opens the database
// directly.
func firewallRestore(ctx context.Context, w io.Writer, dbPath string) error {
	st, err := openStore(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := firewall.RestoreConfirmed(ctx, st, firewall.ExecCmd{}); err != nil {
		return fmt.Errorf("firewall restore: %w", err)
	}
	reg, err := settings.New(st)
	if err != nil {
		return fmt.Errorf("firewall restore: %w", err)
	}
	port, err := reg.Int(ctx, "panel.port")
	if err != nil {
		return fmt.Errorf("firewall restore: panel.port: %w", err)
	}
	if err := openPanelPort(ctx, port); err != nil {
		return fmt.Errorf("firewall restore: panel port %d: %w", port, err)
	}
	fmt.Fprintln(w, "firewall: confirmed ruleset restored (nothing happens when the firewall is disabled); panel port open")
	return nil
}

// firewallDisable is the console recovery path for an operator who cannot reach
// the panel: it unhooks and flushes the panel chains and forgets the saved ruleset.
// ufw and firewalld stay masked.
func firewallDisable(ctx context.Context, w io.Writer, dbPath string) error {
	st, err := openStore(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := firewall.DisableAll(ctx, st, firewall.ExecCmd{}); err != nil {
		return fmt.Errorf("firewall disable: %w", err)
	}
	fmt.Fprintln(w, "firewall: disabled. The panel chains are flushed and unhooked; the distribution's own firewall services stay masked.")
	return nil
}
