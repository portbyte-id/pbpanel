package jobs

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestListNewestFirstAndActiveOnly pins the list order and the active filter.
// The clock is stepped one second per call so created_at never ties; the
// database breaks ties on a random id, which would make order untestable.
func TestListNewestFirstAndActiveOnly(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "panel.db"))
	m := newManager(t, st)
	clock := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	m.now = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}

	ctx := context.Background()
	release := make(chan struct{})
	long, err := m.Start(ctx, "long.running", func(ctx context.Context, out *Writer) error {
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Release and wait on every path, including a failed assertion, so the
	// blocked goroutine never outlives the store.
	defer func() {
		close(release)
		if _, err := m.Wait(ctx, long.ID); err != nil {
			t.Errorf("Wait(long): %v", err)
		}
	}()

	quick := runJob(t, m, "quick.done", writeLines(1))

	all, err := m.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 || all[0].ID != quick.ID || all[1].ID != long.ID {
		t.Fatalf("List all = %v; want [quick, long] newest first", ids(all))
	}

	active, err := m.List(ctx, ListOptions{Active: true})
	if err != nil {
		t.Fatalf("List active: %v", err)
	}
	if len(active) != 1 || active[0].ID != long.ID || active[0].State != StateRunning {
		t.Fatalf("List active = %v; want only the running job", ids(active))
	}
}

// TestListLimitKeepsNewest checks that a limit trims from the old end, so the
// page still shows what happened most recently.
func TestListLimitKeepsNewest(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "panel.db"))
	m := newManager(t, st)
	clock := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	m.now = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}

	var newest Job
	for range 3 {
		newest = runJob(t, m, "quick.done", writeLines(1))
	}

	got, err := m.List(context.Background(), ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].ID != newest.ID {
		t.Fatalf("List limit 2 = %v; want two jobs with the newest first", ids(got))
	}
}

func ids(jobs []Job) []string {
	out := make([]string, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.ID+":"+string(j.State))
	}
	return out
}
