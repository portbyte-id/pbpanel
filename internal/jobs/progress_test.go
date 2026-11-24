package jobs

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunningJobReportsItsProgressThenClearsIt(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "panel.db"))
	m := newManager(t, st)

	reported := make(chan struct{})
	release := make(chan struct{})
	job, err := m.Start(context.Background(), "install", func(ctx context.Context, out *Writer) error {
		ReportProgress(ctx, "downloading pkg.tar.gz", 50, 200)
		close(reported)
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-reported

	live, err := m.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if live.Progress == nil || live.Progress.Label != "downloading pkg.tar.gz" || live.Progress.Done != 50 || live.Progress.Total != 200 {
		t.Fatalf("running job progress = %+v", live.Progress)
	}

	listed, err := m.List(context.Background(), ListOptions{Active: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].Progress == nil || listed[0].Progress.Done != 50 {
		t.Fatalf("List did not carry the progress: %+v", listed)
	}

	close(release)
	done, err := m.Wait(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if done.State != StateSucceeded || done.Progress != nil {
		t.Fatalf("finished job = %+v, want succeeded with no progress", done)
	}
}

func TestReportProgressOutsideAJobDoesNothing(t *testing.T) {
	// No run in the context: the call must be a harmless no-op, not a panic.
	ReportProgress(context.Background(), "x", 1, 2)
}

func TestProgressReaderReportsBytesAsTheyArrive(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "panel.db"))
	m := newManager(t, st)

	var seen []int64
	job, err := m.Start(context.Background(), "download", func(ctx context.Context, out *Writer) error {
		body := ProgressReader(ctx, "downloading", 10, strings.NewReader("0123456789"))
		buf := make([]byte, 4)
		for {
			n, rerr := body.Read(buf)
			if n > 0 {
				if p := m.liveProgress(out.r.id); p != nil {
					seen = append(seen, p.Done)
				}
			}
			if rerr == io.EOF {
				return nil
			}
			if rerr != nil {
				return rerr
			}
		}
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.Wait(context.Background(), job.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(seen) != 3 || seen[0] != 4 || seen[2] != 10 {
		t.Fatalf("bytes reported = %v, want [4 8 10]", seen)
	}
}
