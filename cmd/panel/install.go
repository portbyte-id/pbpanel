package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/osdep"
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/version"
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

// upgradePanel implements `pbp upgrade`: download the latest GitHub release
// (verified against SHA256SUMS), replace the installed binary with it, restart,
// and roll back if it does not answer.
//
// --local keeps the old behaviour of installing the running executable, for a
// binary the operator copied onto the host by hand. --version <tag> picks a
// release other than the latest.
func upgradePanel(ctx context.Context, stdout io.Writer, args []string) error {
	local := false
	tag := "latest"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--local":
			local = true
		case "--version":
			if i+1 >= len(args) {
				return errors.New("upgrade: --version needs a tag")
			}
			i++
			tag = args[i]
		default:
			return fmt.Errorf("upgrade: unknown option %q", args[i])
		}
	}
	if local && tag != "latest" {
		return errors.New("upgrade: --local and --version cannot be combined")
	}

	u := &install.Upgrader{
		Layout: defaultLayout(),
		Runner: osdep.DefaultRunner(),
		Out:    stdout,
	}

	if !local {
		token := os.Getenv("PB_GITHUB_TOKEN")
		if token == "" {
			token = os.Getenv("GITHUB_TOKEN")
		}
		rc := &install.ReleaseClient{Repo: os.Getenv("PB_REPO"), Token: token, Version: tag, Out: stdout}

		printf(stdout, "  looking up the %s release\n", tag)
		latest, err := rc.LatestTag(ctx)
		if err != nil {
			return fmt.Errorf("upgrade: %w", err)
		}
		if latest == version.Version && os.Getenv("PB_FORCE") != "1" {
			printf(stdout, "already up to date (%s); set PB_FORCE=1 to reinstall\n", latest)
			return nil
		}
		printf(stdout, "  downloading %s\n", latest)
		rel, err := rc.Fetch(ctx)
		if err != nil {
			return fmt.Errorf("upgrade: %w", err)
		}
		defer rel.Cleanup()
		u.SourcePath = rel.Path
	}
	result, err := u.Upgrade(ctx)
	if err != nil {
		return err
	}
	if result.RolledBack {
		return errors.New("upgrade: rolled back to the previous binary")
	}
	if len(result.Migrated) > 0 {
		versions := make([]string, len(result.Migrated))
		for i, v := range result.Migrated {
			versions[i] = strconv.Itoa(v)
		}
		printf(stdout, "\nmigrated to schema version(s) %s\n", strings.Join(versions, ", "))
	}
	printf(stdout, "upgrade complete\n")
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

// readLine reads one line, without its newline, from r.
func readLine(r io.Reader) (string, bool) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimRight(line, "\r\n"), true
}
