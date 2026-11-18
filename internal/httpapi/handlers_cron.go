package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/portbyte-id/server-panel/internal/cron"
)

// CronService is the part of *cron.Service the routes use; tests substitute a
// fake.
type CronService interface {
	List(ctx context.Context) ([]cron.Job, error)
	Get(ctx context.Context, id int64) (cron.Job, error)
	Create(ctx context.Context, spec cron.JobSpec) (cron.Job, error)
	Update(ctx context.Context, id int64, spec cron.JobSpec) (cron.Job, error)
	Delete(ctx context.Context, id int64) error
	SetEnabled(ctx context.Context, id int64, enabled bool) (cron.Job, error)
	RunNow(ctx context.Context, id int64) (cron.RunNowResult, error)
	Runs(ctx context.Context, jobID int64, limit int) ([]cron.Run, error)
	GetRun(ctx context.Context, runID int64) (cron.Run, error)
}

const errShellNotAllowed = "shell_not_allowed"

// cronRoutes registers the cron routes of development plan step 7.7.
//
// The run detail route is /api/cron/{id}/runs/{runId}, not /api/cron/runs/{runId}:
// the mux refuses both that and /api/cron/{id}/runs, because /api/cron/runs/runs
// would match either.
func (s *server) cronRoutes(mux *http.ServeMux) {
	h := func(fn func(CronService, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Cron == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"scheduled jobs are not available; see the daemon log")
				return
			}
			fn(s.deps.Cron, w, r)
		}))
	}
	mux.Handle("GET /api/cron", h(s.cronList))
	mux.Handle("POST /api/cron", h(s.cronCreate))
	mux.Handle("GET /api/cron/{id}", h(s.cronGet))
	mux.Handle("PUT /api/cron/{id}", h(s.cronUpdate))
	mux.Handle("DELETE /api/cron/{id}", h(s.cronDelete))
	mux.Handle("POST /api/cron/{id}/enable", h(s.cronEnable))
	mux.Handle("POST /api/cron/{id}/disable", h(s.cronDisable))
	mux.Handle("POST /api/cron/{id}/run", h(s.cronRun))
	mux.Handle("GET /api/cron/{id}/runs", h(s.cronRuns))
	mux.Handle("GET /api/cron/{id}/runs/{runId}", h(s.cronRunGet))
}

func (s *server) cronFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, cron.ErrNotFound):
		writeError(w, http.StatusNotFound, errNotFound, "no such job")
	case errors.Is(err, cron.ErrRunNotFound):
		writeError(w, http.StatusNotFound, errNotFound, "no such run")
	case errors.Is(err, cron.ErrDuplicate):
		writeJSON(w, http.StatusConflict, errorEnvelope{Error: errConflict,
			Detail: "a job with that name already exists", Field: "name"})
	case errors.Is(err, cron.ErrBuiltin):
		writeJSON(w, http.StatusConflict, errorEnvelope{Error: errConflict, Detail: err.Error()})
	case errors.Is(err, cron.ErrShellNotAllowed):
		writeError(w, http.StatusForbidden, errShellNotAllowed,
			"shell jobs are turned off; enable cron.allow_shell in Settings to use them")
	case errors.Is(err, cron.ErrInvalid):
		// The validation text names the offending field and quotes only what the
		// client sent.
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errInvalidInput, Detail: err.Error()})
	default:
		s.writeInternal(w, r, err)
	}
}

// cronJobInput is the body of POST and PUT /api/cron. The names match the job
// the API returns.
type cronJobInput struct {
	Name           string       `json:"name"`
	Schedule       string       `json:"schedule"`
	Kind           cron.Kind    `json:"kind"`
	User           string       `json:"user"`
	Argv           []string     `json:"argv"`
	ScriptPath     string       `json:"script_path"`
	ScriptBody     string       `json:"script_body"`
	Workdir        string       `json:"workdir"`
	TimeoutSeconds int          `json:"timeout_seconds"`
	Overlap        cron.Overlap `json:"overlap"`
	Retention      int          `json:"retention"`
	Enabled        *bool        `json:"enabled"`
}

func (in cronJobInput) spec() cron.JobSpec {
	return cron.JobSpec{
		Name: in.Name, Schedule: in.Schedule, Kind: in.Kind, User: in.User, Argv: in.Argv,
		ScriptPath: in.ScriptPath, ScriptBody: in.ScriptBody, Workdir: in.Workdir,
		TimeoutSeconds: in.TimeoutSeconds, Overlap: in.Overlap, Retention: in.Retention, Enabled: in.Enabled,
	}
}

// cronRunBrief is a run without its output, for the job list.
type cronRunBrief struct {
	ID         int64       `json:"id"`
	Source     cron.Source `json:"source"`
	Status     cron.Status `json:"status"`
	ExitCode   int         `json:"exit_code"`
	StartedAt  time.Time   `json:"started_at"`
	FinishedAt *time.Time  `json:"finished_at,omitempty"`
}

// cronJobView is a job with what the list needs beside it.
type cronJobView struct {
	cron.Job
	// NextRun is the next scheduled firing, null for a disabled job or a
	// schedule that never fires.
	NextRun *time.Time `json:"next_run"`
	// LastRun is the newest run, null for a job that has never run.
	LastRun *cronRunBrief `json:"last_run"`
}

func (s *server) cronView(ctx context.Context, c CronService, j cron.Job) cronJobView {
	v := cronJobView{Job: j}
	if j.Enabled {
		if next, ok := j.Next(time.Now()); ok {
			v.NextRun = &next
		}
	}
	// A failed history read leaves the row without a last run rather than
	// failing the whole list.
	if runs, err := c.Runs(ctx, j.ID, 1); err == nil && len(runs) > 0 {
		r := runs[0]
		v.LastRun = &cronRunBrief{ID: r.ID, Source: r.Source, Status: r.Status, ExitCode: r.ExitCode,
			StartedAt: r.StartedAt, FinishedAt: r.FinishedAt}
	}
	return v
}

func cronID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusNotFound, errNotFound, "no such job")
		return 0, false
	}
	return id, true
}

func (s *server) cronList(c CronService, w http.ResponseWriter, r *http.Request) {
	jobs, err := c.List(r.Context())
	if err != nil {
		s.cronFail(w, r, err)
		return
	}
	out := make([]cronJobView, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, s.cronView(r.Context(), c, j))
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

func (s *server) cronGet(c CronService, w http.ResponseWriter, r *http.Request) {
	id, ok := cronID(w, r)
	if !ok {
		return
	}
	j, err := c.Get(r.Context(), id)
	if err != nil {
		s.cronFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.cronView(r.Context(), c, j))
}

func (s *server) cronCreate(c CronService, w http.ResponseWriter, r *http.Request) {
	var in cronJobInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	noteTarget(r, "cron:"+in.Name)
	note(r, "kind", string(in.Kind))
	j, err := c.Create(r.Context(), in.spec())
	if err != nil {
		s.cronFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.cronView(r.Context(), c, j))
}

func (s *server) cronUpdate(c CronService, w http.ResponseWriter, r *http.Request) {
	id, ok := cronID(w, r)
	if !ok {
		return
	}
	var in cronJobInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	noteTarget(r, "cron:"+strconv.FormatInt(id, 10))
	note(r, "kind", string(in.Kind))
	j, err := c.Update(r.Context(), id, in.spec())
	if err != nil {
		s.cronFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.cronView(r.Context(), c, j))
}

func (s *server) cronDelete(c CronService, w http.ResponseWriter, r *http.Request) {
	id, ok := cronID(w, r)
	if !ok {
		return
	}
	noteTarget(r, "cron:"+strconv.FormatInt(id, 10))
	if err := c.Delete(r.Context(), id); err != nil {
		s.cronFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) cronEnable(c CronService, w http.ResponseWriter, r *http.Request) {
	s.cronSetEnabled(c, w, r, true)
}

func (s *server) cronDisable(c CronService, w http.ResponseWriter, r *http.Request) {
	s.cronSetEnabled(c, w, r, false)
}

func (s *server) cronSetEnabled(c CronService, w http.ResponseWriter, r *http.Request, enabled bool) {
	id, ok := cronID(w, r)
	if !ok {
		return
	}
	if !fwEmptyBody(w, r) {
		return
	}
	noteTarget(r, "cron:"+strconv.FormatInt(id, 10))
	j, err := c.SetEnabled(r.Context(), id, enabled)
	if err != nil {
		s.cronFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.cronView(r.Context(), c, j))
}

type cronRunResponse struct {
	Outcome cron.Outcome `json:"outcome"`
	// Run is null when the firing was queued behind a run in progress: a queued
	// firing has no run until the running one ends.
	Run *cron.Run `json:"run"`
}

// cronRun starts a job now. 202 when it started or was queued (it finishes in
// the background; poll the run), 200 when the overlap policy skipped it and
// recorded a skipped run.
func (s *server) cronRun(c CronService, w http.ResponseWriter, r *http.Request) {
	id, ok := cronID(w, r)
	if !ok {
		return
	}
	if !fwEmptyBody(w, r) {
		return
	}
	noteTarget(r, "cron:"+strconv.FormatInt(id, 10))
	res, err := c.RunNow(r.Context(), id)
	if err != nil {
		s.cronFail(w, r, err)
		return
	}
	note(r, "outcome", string(res.Outcome))
	out := cronRunResponse{Outcome: res.Outcome}
	if res.Outcome != cron.OutcomeQueued {
		run := res.Run
		out.Run = &run
		note(r, "run_id", run.ID)
	}
	status := http.StatusAccepted
	if res.Outcome == cron.OutcomeSkipped {
		status = http.StatusOK
	}
	writeJSON(w, status, out)
}

func (s *server) cronRuns(c CronService, w http.ResponseWriter, r *http.Request) {
	id, ok := cronID(w, r)
	if !ok {
		return
	}
	limit, ok := queryInt(w, r, "limit")
	if !ok {
		return
	}
	runs, err := c.Runs(r.Context(), id, limit)
	if err != nil {
		s.cronFail(w, r, err)
		return
	}
	if runs == nil {
		runs = []cron.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *server) cronRunGet(c CronService, w http.ResponseWriter, r *http.Request) {
	id, ok := cronID(w, r)
	if !ok {
		return
	}
	runID, err := strconv.ParseInt(r.PathValue("runId"), 10, 64)
	if err != nil || runID < 1 {
		writeError(w, http.StatusNotFound, errNotFound, "no such run")
		return
	}
	run, err := c.GetRun(r.Context(), runID)
	if err != nil {
		s.cronFail(w, r, err)
		return
	}
	// A run is only addressable through its own job.
	if run.JobID != id {
		writeError(w, http.StatusNotFound, errNotFound, "no such run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}
