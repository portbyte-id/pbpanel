package install

import (
	"fmt"
	"strings"
)

// unitName is the systemd unit install writes and enables.
const unitName = "panel.service"

// firewallUnitName is the oneshot unit that restores the confirmed firewall ruleset
// at boot, before the network comes up and without the daemon (development plan 4.6).
const firewallUnitName = "panel-firewall.service"

// FirewallUnitFile renders panel-firewall.service. It replays the saved ruleset from
// the database, so the host is filtered before any service listens and does not
// depend on panel.service starting. It is installed enabled but never started by
// install: with the firewall off in the database it does nothing at boot.
func FirewallUnitFile(binaryPath string) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Server Panel firewall restore\n")
	// No default dependencies: the unit must run before network-pre.target, which
	// the default sysinit ordering would put it after. The database lives under
	// /www, so that mount has to be there first.
	b.WriteString("DefaultDependencies=no\n")
	b.WriteString("RequiresMountsFor=/www\n")
	b.WriteString("After=local-fs.target\n")
	b.WriteString("Before=network-pre.target\n")
	b.WriteString("Wants=network-pre.target\n")
	b.WriteString("Conflicts=shutdown.target\n")
	b.WriteString("Before=shutdown.target\n")
	b.WriteString("\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=oneshot\n")
	b.WriteString("RemainAfterExit=yes\n")
	fmt.Fprintf(&b, "ExecStart=%s firewall restore\n", binaryPath)
	b.WriteString("\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// UnitFile renders the panel's systemd unit from plan.md §3.2.
//
// The hardening set is exactly that section: NoNewPrivileges, LockPersonality,
// RestrictRealtime, ProtectClock, ProtectHostname, SystemCallArchitectures=native,
// MemoryDenyWriteExecute and Restart=always.
//
// Filesystem protections (ProtectSystem, ProtectHome, ReadOnlyPaths) are left
// out on purpose. The file manager has to write anywhere on the host, and a unit
// that restricted it would turn every file operation outside /www into a
// permission error the operator cannot explain from the UI. That is a documented
// limit, not an oversight: this unit narrows escalation paths, it does not
// contain a compromised session, and authentication is the real boundary.
//
// Type=simple with Restart=always, because a panel that dies must come back on
// its own: there is no supervisor above it.
func UnitFile(binaryPath string) string {
	var b strings.Builder

	b.WriteString("[Unit]\n")
	b.WriteString("Description=Server Panel\n")
	// Docker has to be up before the panel renders any site config, and the
	// panel does not manage its own start order.
	b.WriteString("After=network-online.target docker.service\n")
	b.WriteString("Wants=network-online.target\n")
	b.WriteString("\n")

	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s serve\n", binaryPath)
	b.WriteString("Restart=always\n")
	// A short delay keeps a binary that cannot start from being restarted in a
	// tight loop and filling the journal.
	b.WriteString("RestartSec=2s\n")
	b.WriteString("KillMode=mixed\n")
	// The daemon writes its own logs; journald still catches anything before
	// it can, including a failure to start.
	b.WriteString("StandardOutput=journal\n")
	b.WriteString("StandardError=journal\n")
	b.WriteString("\n")

	b.WriteString("# Hardening (plan.md §3.2). Filesystem protections are off by design:\n")
	b.WriteString("# the file manager must be able to write anywhere on the host.\n")
	b.WriteString("NoNewPrivileges=yes\n")
	b.WriteString("LockPersonality=yes\n")
	b.WriteString("RestrictRealtime=yes\n")
	b.WriteString("ProtectClock=yes\n")
	b.WriteString("ProtectHostname=yes\n")
	b.WriteString("SystemCallArchitectures=native\n")
	b.WriteString("MemoryDenyWriteExecute=yes\n")
	b.WriteString("\n")

	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}
