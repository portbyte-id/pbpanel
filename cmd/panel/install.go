package main

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/osdep"
	"github.com/portbyte-id/server-panel/internal/store"
)

// installPanel implements `pbp install`: the step list in internal/install,
// with the production layout, the real host and the real runner.
//
// The password is read from stdin only when the account does not exist yet and
// stdin is a terminal. A non-interactive run (a smoke script, a provisioning
// tool) gets a generated password printed once instead of a prompt that would
// block forever.
func installPanel(ctx context.Context, stdout io.Writer) error {
	in := &install.Installer{
		Layout:          defaultLayout(),
		OS:              osdep.NewUbuntu(osdep.DefaultRunner()),
		Runner:          osdep.DefaultRunner(),
		Out:             stdout,
		PanelPortOpener: openPanelPort,
	}

	// The prompt is decided before the run: once the admin account exists the
	// installer never reads Password, so asking would be a lie about what
	// happens next.
	if needsPassword(ctx, in.Layout) {
		if password, ok := promptPassword(stdout); ok {
			in.Password = password
		}
	}

	report, err := in.Install(ctx)
	if err != nil {
		return err
	}

	skipped := 0
	for _, s := range report.Steps {
		if s.Skipped {
			skipped++
		}
	}
	printf(stdout, "\n%d of %d steps were already done.\n", skipped, len(report.Steps))
	return nil
}

// defaultLayout is the production layout, with the root overridable through the
// same PB_PANEL_DATA_DIR the recovery commands use, so one environment variable
// points every CLI command at the same tree.
func defaultLayout() install.Layout {
	l := install.DefaultLayout()
	dir := os.Getenv(dataDirEnv)
	if dir == "" {
		return l
	}
	// The variable names the data directory; the root is two levels up
	// (<root>/panel/data).
	l.Root = strings.TrimSuffix(strings.TrimSuffix(dir, "/data"), "/panel")
	return l
}

// needsPassword reports whether this install will create the admin account, and
// so whether asking for a password is meaningful.
func needsPassword(ctx context.Context, l install.Layout) bool {
	if _, err := os.Stat(l.DBPath()); err != nil {
		return true
	}
	st, err := store.Open(l.DBPath())
	if err != nil {
		return false
	}
	defer func() { _ = st.Close() }()

	has, err := newAuthService(st).HasAdmin(ctx)
	return err == nil && !has
}

// promptPassword asks for a password with echo off and returns it only when the
// operator typed the same value twice. Anything else — no terminal, EOF, three
// mismatches — returns ok=false, and the installer generates one instead. A
// refused prompt must never leave the account with an empty password.
func promptPassword(stdout io.Writer) (string, bool) {
	printf(stdout, "\nSet the admin password (leave empty to generate one).\n")
	for range 3 {
		printf(stdout, "password: ")
		first, ok := readPassword()
		printf(stdout, "\n")
		if !ok || first == "" {
			return "", false
		}
		printf(stdout, "again:    ")
		second, ok := readPassword()
		printf(stdout, "\n")
		if !ok {
			return "", false
		}
		if first == second {
			return first, true
		}
		printf(stdout, "the two entries differ; try again\n")
	}
	printf(stdout, "no matching password after three tries; generating one instead\n")
	return "", false
}
