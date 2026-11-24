package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/osdep"
	"github.com/portbyte-id/server-panel/internal/release"
	"github.com/portbyte-id/server-panel/internal/update"
	"github.com/portbyte-id/server-panel/internal/version"
)

// upgradePanel implements `pbp upgrade`: download the latest GitHub release
// (verified against SHA256SUMS), replace the installed binary with it, restart,
// and roll back if it does not answer.
//
// --local keeps the old behaviour of installing the running executable, for a
// binary the operator copied onto the host by hand. --version <tag> picks a
// release other than the latest. --status-file is set by the panel, which starts
// the upgrade as its own unit: the file is where the panel reads progress from,
// because the restart this upgrade performs ends any request waiting on it.
func upgradePanel(ctx context.Context, stdout io.Writer, args []string) (retErr error) {
	local := false
	tag := "latest"
	statusPath := ""
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
		case "--status-file":
			if i+1 >= len(args) {
				return errors.New("upgrade: --status-file needs a path")
			}
			i++
			statusPath = args[i]
		default:
			return fmt.Errorf("upgrade: unknown option %q", args[i])
		}
	}
	if local && tag != "latest" {
		return errors.New("upgrade: --local and --version cannot be combined")
	}

	var (
		rep *update.Reporter
		out = stdout
	)
	if statusPath != "" {
		r, err := update.OpenReporter(statusPath)
		if err != nil {
			return fmt.Errorf("upgrade: %w", err)
		}
		rep = r
		out = io.MultiWriter(stdout, rep.Writer())
		// Whatever ends the upgrade, the status file records how it ended.
		defer func() {
			if ferr := rep.Finish(retErr); ferr != nil && retErr == nil {
				retErr = fmt.Errorf("upgrade: record the result: %w", ferr)
			}
		}()
	}
	phase := func(p, detail string) {
		if rep != nil {
			_ = rep.Phase(p, detail)
		}
	}

	u := &install.Upgrader{
		Layout: defaultLayout(),
		Runner: osdep.DefaultRunner(),
		Out:    out,
		Report: phase,
	}

	if !local {
		token := os.Getenv("PB_GITHUB_TOKEN")
		if token == "" {
			token = os.Getenv("GITHUB_TOKEN")
		}
		rc := &release.ReleaseClient{Repo: os.Getenv("PB_REPO"), Token: token, Version: tag, Out: out}

		phase(update.PhaseChecking, "looking up the "+tag+" release")
		printf(out, "  looking up the %s release\n", tag)
		meta, err := rc.Meta(ctx)
		if err != nil {
			return fmt.Errorf("upgrade: %w", err)
		}
		if meta.Tag == version.Version && os.Getenv("PB_FORCE") != "1" {
			phase(update.PhaseUpToDate, "already running "+meta.Tag)
			printf(out, "already up to date (%s); set PB_FORCE=1 to reinstall\n", meta.Tag)
			return nil
		}

		phase(update.PhaseDownloading, "downloading "+meta.Tag)
		printf(out, "  downloading %s\n", meta.Tag)
		if rep != nil {
			rc.Progress = func(done, total int64) { rep.Progress(done, total) }
		}
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
		printf(out, "\nmigrated to schema version(s) %s\n", strings.Join(versions, ", "))
	}
	printf(out, "upgrade complete\n")
	return nil
}
