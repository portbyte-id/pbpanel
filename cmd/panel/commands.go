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
			usage:   "pbp serve",
			maxArgs: -1, // daemon flags arrive with step 0.7
			run:     serve,
		},
		{
			name:    "install",
			summary: "install the panel on this host",
			usage:   "pbp install",
			run: func(ctx context.Context, _ []string) error {
				return installPanel(ctx, stdout)
			},
		},
		{
			name:    "upgrade",
			summary: "download the latest release, replace the binary and migrate",
			usage:   "pbp upgrade [--local | --version <tag>]",
			maxArgs: 2,
			run: func(ctx context.Context, args []string) error {
				return upgradePanel(ctx, stdout, args)
			},
		},
		{
			name:    "info",
			summary: "show port, URL, version and database path",
			usage:   "pbp info",
			run: func(ctx context.Context, _ []string) error {
				return info(ctx, stdout, databasePath())
			},
		},
		{
			name:    "reset-password",
			summary: "set a new admin password and revoke sessions",
			usage:   "pbp reset-password",
			run: func(ctx context.Context, _ []string) error {
				return resetPassword(ctx, stdout, databasePath())
			},
		},
		{
			name:    "unlock-login",
			summary: "clear failed-login lockouts on the running panel",
			usage:   "pbp unlock-login",
			run: func(ctx context.Context, _ []string) error {
				return unlockLogin(ctx, stdout, databasePath())
			},
		},
		{
			name:    "set",
			summary: "change one setting through the registry validators",
			usage:   "pbp set <key> <value>",
			help:    setHelp,
			minArgs: 2,
			maxArgs: 2,
			run: func(ctx context.Context, args []string) error {
				return setSetting(ctx, stdout, databasePath(), args[0], args[1])
			},
		},
		{
			name:    "firewall restore",
			summary: "restore the saved ruleset at boot",
			usage:   "pbp firewall restore",
			run: func(ctx context.Context, _ []string) error {
				return firewallRestore(ctx, stdout, databasePath())
			},
		},
		{
			name:    "firewall disable",
			summary: "unhook and flush the panel chains from a console",
			usage:   "pbp firewall disable",
			run: func(ctx context.Context, _ []string) error {
				return firewallDisable(ctx, stdout, databasePath())
			},
		},
		{
			name:    "domain-binding remove",
			summary: "clear the panel domain binding from a console",
			usage:   "pbp domain-binding remove",
			run: func(ctx context.Context, _ []string) error {
				return removeDomainBinding(ctx, stdout, databasePath())
			},
		},
		{
			name:    "version",
			summary: "print the version and exit",
			usage:   "pbp version",
			noRoot:  true,
			run: func(context.Context, []string) error {
				_, err := fmt.Fprintln(stdout, version.Version)
				return err
			},
		},
	}
}
