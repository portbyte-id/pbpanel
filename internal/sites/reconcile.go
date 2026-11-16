package sites

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/nginx"
)

// Runtime is the nginx container as the reconciler sees it.
type Runtime interface {
	ImagePresent(ctx context.Context) (bool, error)
	EnsureContainer(ctx context.Context) (docker.EnsureResult, error)
}

// Step is one stage of a reconcile and how it ended.
type Step struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	OK     bool   `json:"ok"`
}

// Status is the outcome of the last reconcile, for `GET /api/services/nginx`.
type Status struct {
	Done  bool      `json:"done"`
	OK    bool      `json:"ok"`
	Steps []Step    `json:"steps"`
	At    time.Time `json:"at"`
	// Error and Output are set when a step failed: Output is the validator's
	// text, verbatim.
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"`
}

// Reconciler brings nginx back to what the database says, once, at daemon
// start. It never touches a site's own file: those belong to the operator.
type Reconciler struct {
	Sites  *Manager
	Global *nginx.Global
	// Recover undoes applies that were in flight when the daemon last stopped
	// (confedit.Editor.Recover). It runs before anything reloads.
	Recover func() ([]string, error)
	// Boot validates without reloading: nginx is not running yet when the files
	// it will start with are written.
	Boot confedit.Service
	// Live validates, reloads and health-checks the running container.
	Live    confedit.Service
	Runtime Runtime
	// BuildImage builds the derived nginx image when it is missing. It is a
	// field because a build is a job with visible progress, not a side effect
	// of reconciling.
	BuildImage func(ctx context.Context) error
	Logger     *slog.Logger

	mu     sync.Mutex
	status Status
}

// Status returns the outcome of the last Run.
func (r *Reconciler) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.status
	st.Steps = append([]Step(nil), st.Steps...)
	return st
}

// Run reconciles and records the result. A failure stops the run and is
// reported rather than retried: the panel keeps serving, and the operator sees
// what is wrong.
func (r *Reconciler) Run(ctx context.Context) Status {
	st := Status{At: time.Now().UTC()}
	err := r.run(ctx, &st)
	st.Done, st.OK = true, err == nil
	if err != nil {
		st.Error = err.Error()
		var ae *confedit.ApplyError
		if errors.As(err, &ae) {
			st.Output = ae.Output
		}
		var oe *outputError
		if errors.As(err, &oe) {
			st.Output = oe.output
		}
		if r.Logger != nil {
			r.Logger.Error("nginx reconcile failed", "error", err, "output", st.Output)
		}
	}
	r.mu.Lock()
	r.status = st
	r.mu.Unlock()
	return st
}

// outputError carries a validator's output out of a step.
type outputError struct {
	err    error
	output string
}

func (e *outputError) Error() string { return e.err.Error() }
func (e *outputError) Unwrap() error { return e.err }

func (r *Reconciler) run(ctx context.Context, st *Status) error {
	step := func(name, detail string, err error) error {
		st.Steps = append(st.Steps, Step{Name: name, Detail: detail, OK: err == nil})
		return err
	}
	logf := func(msg string, args ...any) {
		if r.Logger != nil {
			r.Logger.Info(msg, args...)
		}
	}

	// 1. An unfinished apply left unvalidated files on disk.
	recovered, err := r.Recover()
	if err := step("recover", fmt.Sprintf("%d file(s) restored from an unfinished apply", len(recovered)), err); err != nil {
		return err
	}
	if len(recovered) > 0 {
		logf("restored files from an unfinished apply", "paths", recovered)
	}

	// 2. The image. A missing one is built as a job; reconciling does not pull.
	present, err := r.Runtime.ImagePresent(ctx)
	if err != nil {
		return step("image", "", err)
	}
	if !present {
		if r.BuildImage == nil {
			return step("image", "", fmt.Errorf("the nginx image %s is missing and nothing can build it", nginx.Image))
		}
		if err := step("image", "building "+nginx.Image, r.BuildImage(ctx)); err != nil {
			return err
		}
	} else {
		_ = step("image", nginx.Image+" is present", nil)
	}

	// 3. Panel-owned files, from the database. Only files that are missing or
	// differ are written, so a daemon restart on a healthy host writes nothing.
	want, err := r.desired(ctx)
	if err != nil {
		return step("render", "", err)
	}
	changed, err := r.differing(want)
	if err != nil {
		return step("render", "", err)
	}
	if len(changed) > 0 {
		_, err = r.Sites.ap.Apply(ctx, confedit.Change{Files: changed, Services: []confedit.Service{r.Boot}, Reason: "boot reconcile"})
		if err := step("render", fmt.Sprintf("%d file(s) written and validated", len(changed)), err); err != nil {
			return err
		}
	} else {
		// Nothing written, but the raw files may have been edited while the
		// panel was down; a container started on a broken config would loop.
		out, err := r.Boot.Validate(ctx)
		if err != nil {
			return step("render", "the configuration on disk is rejected by nginx -t", &outputError{err, out})
		}
		_ = step("render", "all panel-owned files are current", nil)
	}

	// 4. The container.
	ens, err := r.Runtime.EnsureContainer(ctx)
	if err := step("container", string(ens.Action)+": "+ens.Reason, err); err != nil {
		return err
	}

	// 5. A container that was already running has not read the files just
	// written; one that was created or started has.
	if ens.Action == docker.ActionNone {
		if out, err := r.Live.Validate(ctx); err != nil {
			return step("reload", "", &outputError{err, out})
		}
		if err := step("reload", "reloaded once", r.Live.Reload(ctx)); err != nil {
			return err
		}
		return step("health", "", r.Live.Healthy(ctx))
	}
	return step("health", "nginx was started with the current files", r.Live.Healthy(ctx))
}

// desired is every file the panel owns, rendered from the database: nginx.conf
// only when it does not exist, the settings files, and each site's includes.
func (r *Reconciler) desired(ctx context.Context) ([]confedit.File, error) {
	var files []confedit.File

	if _, err := r.Sites.fs.ReadFile(r.Sites.p.ConfPath()); errors.Is(err, fs.ErrNotExist) {
		b, err := r.Sites.p.RenderMainConf()
		if err != nil {
			return nil, err
		}
		files = append(files, confedit.File{Path: r.Sites.p.ConfPath(), Content: b})
	} else if err != nil {
		return nil, err
	}

	s, err := r.Global.Settings(ctx)
	if err != nil {
		return nil, err
	}
	sf, err := r.Global.SettingsFiles(s)
	if err != nil {
		return nil, err
	}
	files = append(files, sf...)

	sites, err := r.Sites.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, site := range sites {
		inc, err := r.Sites.includeFiles(site)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", site.Domain, err)
		}
		files = append(files, inc...)
	}
	return files, nil
}

// differing keeps the files whose content on disk is not already what is wanted.
func (r *Reconciler) differing(want []confedit.File) ([]confedit.File, error) {
	var out []confedit.File
	for _, f := range want {
		have, err := r.Sites.fs.ReadFile(f.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return nil, fmt.Errorf("read %s: %w", f.Path, err)
		case bytes.Equal(have, f.Content):
			continue
		}
		out = append(out, f)
	}
	return out, nil
}
