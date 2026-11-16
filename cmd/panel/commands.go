package main

import (
	"context"
	"fmt"
	"io"

	"github.com/portbyte-id/server-panel/internal/version"
)

// commandSet is the single list of subcommands, in the order they appear in the
// help text. Steps 0.7-0.12 and M4 replace the stub bodies; nothing else here
// changes when they do.
func commandSet(stdout io.Writer) []*command {
	return []*command{
		{
			name:    "serve",
			summary: "run the panel daemon",
			usage:   "panel serve",
			maxArgs: -1, // daemon flags arrive with step 0.7
			run:     serve,
		},
		{
			name:    "install",
			summary: "install the panel on this host (step 0.11)",
			usage:   "panel install",
			run:     notImplemented("install", "0.11"),
		},
		{
			name:    "upgrade",
			summary: "replace the binary and migrate (step 0.11)",
			usage:   "panel upgrade",
			run:     notImplemented("upgrade", "0.11"),
		},
		{
			name:    "info",
			summary: "show port, URL, version and database path (step 0.12)",
			usage:   "panel info",
			run:     notImplemented("info", "0.12"),
		},
		{
			name:    "reset-password",
			summary: "set a new admin password and revoke sessions (step 0.12)",
			usage:   "panel reset-password",
			run:     notImplemented("reset-password", "0.12"),
		},
		{
			name:    "set",
			summary: "change one setting through the registry validators (step 0.12)",
			usage:   "panel set <key> <value>",
			minArgs: 2,
			maxArgs: 2,
			run:     notImplemented("set", "0.12"),
		},
		{
			name:    "firewall restore",
			summary: "restore the saved ruleset at boot (M4)",
			usage:   "panel firewall restore",
			run:     notImplemented("firewall restore", "M4"),
		},
		{
			name:    "firewall disable",
			summary: "unhook and flush the panel chains from a console (M4)",
			usage:   "panel firewall disable",
			run:     notImplemented("firewall disable", "M4"),
		},
		{
			name:    "version",
			summary: "print the version and exit",
			usage:   "panel version",
			noRoot:  true,
			run: func(context.Context, []string) error {
				_, err := fmt.Fprintln(stdout, version.Version)
				return err
			},
		},
	}
}

// serve stays a stub until the daemon lands in steps 0.7 and 0.11. It is a named
// function rather than a closure so that step 0.7 replaces one body.
func serve(context.Context, []string) error {
	return fmt.Errorf("serve: the daemon is not implemented yet (step 0.7)")
}

func notImplemented(name, step string) func(context.Context, []string) error {
	return func(context.Context, []string) error {
		return fmt.Errorf("%s: not implemented yet (step %s)", name, step)
	}
}
