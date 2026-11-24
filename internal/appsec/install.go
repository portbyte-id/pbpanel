package appsec

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// InstallOptions are what one install run needs from its caller.
type InstallOptions struct {
	// Acknowledge must equal Acknowledgement. Anything else is refused before a
	// single byte is downloaded, so an install cannot happen without the operator
	// having read Warning.
	Acknowledge string
	// Progress receives one line per step. Nil discards.
	Progress func(string)
}

// Install downloads the pinned archive, verifies it, unpacks it into a private
// directory, runs the vendor's three installers, writes an all-inactive policy,
// applies it, and reloads nginx.
//
// The order is not incidental. Reloading nginx is last because the attachment
// registers against the agent's /dev/shm sockets only when nginx starts or
// reloads: an agent installed under a running nginx inspects nothing until the
// reload, and the failure is silent (§0.4). The policy is applied before the
// reload so nginx never registers against an agent that would score without a
// policy.
//
// Every step is idempotent, because the vendor's installers are: a re-run upgrades
// in place.
func (m *Manager) Install(ctx context.Context, opts InstallOptions) error {
	if opts.Acknowledge != Acknowledgement {
		return ErrNotAcknowledged
	}
	say := opts.Progress
	if say == nil {
		say = func(string) {}
	}

	rel, err := m.Release()
	if err != nil {
		return err
	}
	say(fmt.Sprintf("open-appsec agent %s (%s)", rel.Version, filepath.Base(rel.URL)))

	// A private directory, mode 0700: the archive is fetched as root and its
	// contents are run as root, so nothing else on the host may write into it. It
	// is removed on every path, including failure.
	dir, err := m.mkdirTemp("", "panel-appsec-*")
	if err != nil {
		return fmt.Errorf("appsec: create a temporary directory: %w", err)
	}
	defer func() { _ = m.removeAll(dir) }()

	archive := filepath.Join(dir, "agent.tar.gz")
	say("downloading the agent archive")
	if err := m.fetch(ctx, rel, archive); err != nil {
		return err
	}
	say(fmt.Sprintf("sha256 matches %s (%d MB)", shortSHA(rel.SHA256), m.fileSize(archive)>>20))

	say("unpacking")
	if out, err := m.run.Run(ctx, UnpackCommand(archive, dir)); err != nil {
		return fmt.Errorf("appsec: unpack the agent archive: %w: %s", err, strings.TrimSpace(string(out)))
	}
	unpacked := filepath.Join(dir, "openappsec")
	if ok, err := m.existingFile(filepath.Join(unpacked, "install-cp-nano-agent.sh")); err != nil {
		return fmt.Errorf("appsec: inspect %s: %w", unpacked, err)
	} else if !ok {
		return fmt.Errorf("appsec: the archive holds no install-cp-nano-agent.sh under %s; it is not the agent archive", unpacked)
	}

	for i, cmd := range InstallCommands(unpacked) {
		say(fmt.Sprintf("running %s (%d of %d)", filepath.Base(cmd.Name), i+1, len(installScripts)))
		if out, err := m.run.Run(ctx, cmd); err != nil {
			return fmt.Errorf("appsec: %s failed: %w: %s", filepath.Base(cmd.Name), err, tail(string(out), 20))
		}
	}

	say("starting the agent")
	if out, err := m.run.Run(ctx, Command{Name: "systemctl", Args: []string{"enable", "--now", m.unitName}}); err != nil {
		return fmt.Errorf("appsec: enable %s: %w: %s", m.unitName, err, strings.TrimSpace(string(out)))
	}
	st, err := m.Status(ctx)
	if err != nil {
		return err
	}
	if !st.Active {
		return fmt.Errorf("appsec: the agent installed but %s is %s: %s", m.unitName, st.State, st.Reason)
	}
	say("the agent is " + st.State + " (version " + st.Version + ")")

	if err := m.writeDefaultPolicy(ctx, say); err != nil {
		return err
	}
	if err := m.applyPolicy(ctx, say); err != nil {
		return err
	}

	// Last, and only when nginx is running: a stopped nginx has nothing to reload
	// and will register at its next start, which is the same guarantee.
	if out, err := m.run.Run(ctx, m.nginxActiveCommand()); err == nil && strings.TrimSpace(string(out)) == "active" {
		say("reloading nginx so it registers against the agent")
		if out, err := m.run.Run(ctx, m.ReloadNginxCommand()); err != nil {
			return fmt.Errorf("appsec: reload nginx: %w: %s", err, strings.TrimSpace(string(out)))
		}
	} else {
		say("nginx is not running; it registers against the agent when it starts")
	}
	say("done")
	return nil
}

// writeDefaultPolicy writes the all-inactive policy when the file is absent or
// holds something else this panel did not render. An unchanged file is not
// rewritten, so the agent reloads nothing.
func (m *Manager) writeDefaultPolicy(_ context.Context, say func(string)) error {
	want := DefaultPolicy()
	path := m.PolicyPath()
	have, err := m.readFile(path)
	switch {
	case err == nil && string(have) == string(want):
		say("the policy already inspects nothing")
		return nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("appsec: read %s: %w", path, err)
	}
	if err := m.mkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("appsec: create %s: %w", filepath.Dir(path), err)
	}
	// 0600: the agent's conf directory is 0700 root-only, and a policy that names
	// hosts and exceptions has no business being world-readable.
	if err := m.writeFile(path, want, 0o600); err != nil {
		return fmt.Errorf("appsec: write %s: %w", path, err)
	}
	say("wrote an all-inactive policy")
	return nil
}

// applyPolicy applies the policy and checks the agent's own verdict.
//
// The check is on the verdict rather than the exit status because the vendor's
// script exits 1 on success: `open-appsec-ctl --apply-policy` ends with `exit 1`
// after printing "New policy applied." in 1.1.37. Trusting the exit code would
// report every successful apply as a failure; trusting only the text would accept
// a script that failed before it printed anything. So the text is required and the
// code is not.
func (m *Manager) applyPolicy(ctx context.Context, say func(string)) error {
	say("applying the policy")
	out, runErr := m.run.Run(ctx, m.ApplyPolicyCommand(m.PolicyPath()))
	text := string(out)
	if !strings.Contains(text, "New policy applied") {
		if runErr != nil {
			return fmt.Errorf("appsec: apply the policy: %w: %s", runErr, tail(text, 20))
		}
		return fmt.Errorf("%w: %s", ErrPolicyRejected, tail(text, 20))
	}
	if st, err := m.Status(ctx); err == nil && st.PolicyLoad == "Error" {
		return fmt.Errorf("%w: the agent reports its last policy load failed", ErrPolicyRejected)
	}
	say("the agent loaded the policy")
	return nil
}

// Uninstall removes the agent. It refuses while any site still runs the engine:
// uninstalling under a site that depends on it would leave that site's config
// naming a module with no agent to register against, which passes every request
// while claiming to inspect them.
func (m *Manager) Uninstall(ctx context.Context, inUse func(context.Context) (bool, error)) error {
	st, err := m.Status(ctx)
	if err != nil {
		return err
	}
	if !st.Installed {
		return ErrNotInstalled
	}
	if inUse != nil {
		used, err := inUse(ctx)
		if err != nil {
			return fmt.Errorf("appsec: check whether a site uses the engine: %w", err)
		}
		if used {
			return ErrInUse
		}
	}
	out, runErr := m.run.Run(ctx, m.UninstallCommand())
	if runErr != nil && !strings.Contains(string(out), "successfully uninstalled") {
		return fmt.Errorf("appsec: uninstall the agent: %w: %s", runErr, tail(string(out), 20))
	}
	if after, err := m.Status(ctx); err == nil && after.Installed {
		return fmt.Errorf("appsec: the uninstaller ran but the agent is still installed at %s", m.root)
	}
	return nil
}

// fileSize is the size of a file, 0 when it cannot be read. It is used for the
// progress line only, so a failure is not worth propagating.
func (m *Manager) fileSize(path string) int64 {
	info, err := m.stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// existingFile reports whether path exists and is a regular file.
func (m *Manager) existingFile(path string) (bool, error) {
	info, err := m.stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

// shortSHA is the first 12 hex characters of a digest, which is enough to name a
// release in a progress line.
func shortSHA(s string) string {
	s = strings.ToLower(s)
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

// tail is the last n lines of a command's output: a vendor script's failure is at
// the end, and a job log line has to stay readable.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
