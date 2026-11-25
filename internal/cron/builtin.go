package cron

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// BuiltinSSLRenew is the built-in that renews the certificates close to expiry.
const BuiltinSSLRenew = "tls.renew"

// BuiltinNotifySweep is the built-in that polls the conditions the notification
// engine raises: firewall off, detached site features, certificates near expiry.
const BuiltinNotifySweep = "notify.sweep"

// BuiltinFunc is what a built-in job runs. It writes its report to out, which
// becomes the run's output; a non-nil error fails the run.
type BuiltinFunc func(ctx context.Context, out io.Writer) error

// defaultJob is a built-in job the panel creates when no job of that name exists.
// Once it exists the operator owns its schedule, enabled state and retention.
type defaultJob struct {
	name     string
	builtin  string
	schedule string
	user     string
	timeout  time.Duration
}

// defaultJobs are seeded on every start that finds them missing. The renewal runs
// at 03:17 as root, because it writes certificate files and reloads nginx.
var defaultJobs = []defaultJob{
	{name: "ssl-renew", builtin: BuiltinSSLRenew, schedule: "17 3 * * *", user: "root", timeout: 2 * time.Hour},
	{name: "notify-sweep", builtin: BuiltinNotifySweep, schedule: "*/5 * * * *", user: "root", timeout: 2 * time.Minute},
}

// EnsureDefaults creates the default built-in jobs that do not exist yet. It is
// safe to call on every start: a job that exists is left exactly as the operator
// has it. A name held by some other job is an error, because the built-in cannot
// be created under it.
func (s *Service) EnsureDefaults(ctx context.Context) error {
	var errs []error
	for _, d := range defaultJobs {
		if err := s.ensureDefault(ctx, d); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) ensureDefault(ctx context.Context, d defaultJob) error {
	if _, ok := s.builtins[d.builtin]; !ok {
		return fmt.Errorf("cron: built-in %q is not registered, so job %q was not created", d.builtin, d.name)
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var kind, builtin string
		err := tx.QueryRowContext(ctx, `SELECT kind, builtin FROM cron_jobs WHERE name = ?`, d.name).Scan(&kind, &builtin)
		if err == nil {
			if kind != string(KindBuiltin) || builtin != d.builtin {
				return fmt.Errorf("cron: a job named %q exists and is not the built-in %q; rename or delete it", d.name, d.builtin)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := s.stamp()
		_, err = tx.ExecContext(ctx, `INSERT INTO cron_jobs
			(name, schedule, kind, run_user, argv, script_path, script_body, workdir, timeout_seconds, overlap, retention, enabled, builtin, created_at, updated_at)
			VALUES (?, ?, ?, ?, '[]', '', '', '', ?, ?, ?, 1, ?, ?, ?)`,
			d.name, d.schedule, KindBuiltin, d.user, int(d.timeout/time.Second), OverlapSkip, s.defRetain, d.builtin, now, now)
		return err
	})
}

// updateBuiltin changes a built-in job. Its schedule, enabled state, overlap,
// timeout and retention can change; its name, kind, account and command cannot.
func (s *Service) updateBuiltin(ctx context.Context, cur Job, spec JobSpec) (Job, error) {
	locked := (spec.Kind != "" && spec.Kind != KindBuiltin) ||
		(spec.User != "" && spec.User != cur.User) ||
		len(spec.Argv) > 0 || spec.ScriptPath != "" || spec.ScriptBody != "" ||
		(spec.Workdir != "" && spec.Workdir != cur.Workdir)
	if locked {
		return Job{}, fmt.Errorf("%w: %s is part of the panel; its kind, account and command cannot be changed", ErrBuiltin, cur.Name)
	}
	if _, err := ParseSchedule(spec.Schedule); err != nil {
		return Job{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	spec.Schedule = strings.TrimSpace(spec.Schedule)
	var err error
	if spec, err = normalizePolicy(spec, s.defRetain); err != nil {
		return Job{}, err
	}
	enabled := cur.Enabled
	if spec.Enabled != nil {
		enabled = *spec.Enabled
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE cron_jobs SET schedule=?, overlap=?, timeout_seconds=?, retention=?, enabled=?, updated_at=? WHERE id=?`,
		spec.Schedule, spec.Overlap, spec.TimeoutSeconds, spec.Retention, boolInt(enabled), s.stamp(), cur.ID); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, cur.ID)
}

// runBuiltin calls fn and turns a panic into an error. A built-in runs inside the
// daemon, so a panic must end its own run and not the panel.
func runBuiltin(ctx context.Context, fn BuiltinFunc, out io.Writer) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("built-in panicked: %v", p)
		}
	}()
	return fn(ctx, out)
}
