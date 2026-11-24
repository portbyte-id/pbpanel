package jobs

import (
	"context"
	"io"
)

// Progress is how far the running step of a job has got: a download, usually.
// It lives in memory on the running job and is dropped when the job ends, so
// the progress bar shows only while there is something to measure.
type Progress struct {
	// Label names the step, for example "downloading nginx-1.27.tar.gz".
	Label string `json:"label"`
	// Done is how much of the step is finished, in the step's own unit.
	Done int64 `json:"done"`
	// Total is how much the step will take, or 0 when that is not known.
	Total int64 `json:"total"`
}

// runKey carries the job's run through the context its body receives, so code
// deep in an install can report progress without a callback threaded through
// every signature between it and the job.
type runKey struct{}

// ReportProgress records progress for the job running under ctx. Outside a job
// it does nothing, so code that is also called from a plain request is unaffected.
func ReportProgress(ctx context.Context, label string, done, total int64) {
	r, ok := ctx.Value(runKey{}).(*run)
	if !ok {
		return
	}
	r.mu.Lock()
	r.progress = &Progress{Label: label, Done: done, Total: total}
	r.mu.Unlock()
}

// ProgressReader wraps r so that every read reports the bytes read so far to the
// job running under ctx. total is the expected size, 0 when it is not known.
func ProgressReader(ctx context.Context, label string, total int64, r io.Reader) io.Reader {
	return &progressReader{ctx: ctx, label: label, total: total, r: r}
}

type progressReader struct {
	ctx   context.Context
	label string
	total int64
	done  int64
	r     io.Reader
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		ReportProgress(p.ctx, p.label, p.done, p.total)
	}
	return n, err
}

// liveProgress returns a copy of the progress of a job still running in this
// process, or nil when the job is not running here.
func (m *Manager) liveProgress(id string) *Progress {
	m.mu.Lock()
	r, ok := m.runs[id]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.progress == nil {
		return nil
	}
	p := *r.progress
	return &p
}
