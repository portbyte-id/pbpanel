package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/cron"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/notify"
	"github.com/portbyte-id/server-panel/internal/secretbox"
	"github.com/portbyte-id/server-panel/internal/store"
)

// notifyFixture is an engine over a fresh store, with one webhook channel that
// accepts every delivery.
func notifyFixture(t *testing.T) (*notify.Service, *notify.Repo, *notify.Channel) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{9}, secretbox.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	repo := notify.NewRepo(st, box, nil)
	svc := notify.New(notify.Options{Repo: repo, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ch, err := repo.CreateChannel(context.Background(), notify.ChannelWrite{
		Name: "hook", Type: notify.TypeWebhook, Enabled: true, MinSeverity: notify.Warning,
		SendResolved: true, Config: map[string]any{"url": srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, repo, &ch
}

// deliveryStates returns the states of a channel's deliveries, oldest first.
func deliveryStates(t *testing.T, repo *notify.Repo, channelID int64) []string {
	t.Helper()
	rows, err := repo.ListDeliveries(context.Background(), notify.DeliveryFilter{ChannelID: channelID, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(rows) - 1; i >= 0; i-- {
		out = append(out, rows[i].State)
	}
	return out
}

// A failing cron run produces one event and one queued delivery; a repeat
// failure inside the dedupe window is suppressed, not queued again.
func TestCronHookFailureQueuesOneDelivery(t *testing.T) {
	svc, repo, ch := notifyFixture(t)
	hook := cronNotifyHook(svc)
	job := cron.Job{Name: "nightly"}

	hook(job, cron.StatusFailed, "exit status 1")
	events, err := repo.ListEvents(context.Background(), notify.EventFilter{})
	if err != nil || len(events) != 1 || events[0].Kind != notify.KindCronFailed {
		t.Fatalf("events = %+v, %v; want one cron.failed", events, err)
	}
	if got := deliveryStates(t, repo, ch.ID); len(got) != 1 || got[0] != "queued" {
		t.Fatalf("deliveries = %v, want one queued", got)
	}

	hook(job, cron.StatusFailed, "exit status 1")
	if got := deliveryStates(t, repo, ch.ID); len(got) != 2 || got[1] != "suppressed" {
		t.Fatalf("second failure deliveries = %v, want queued then suppressed", got)
	}
}

// A success clears only a failure that was reported; with nothing open it adds no bell row.
func TestCronHookSuccessClearsOnlyAnOpenFailure(t *testing.T) {
	svc, repo, ch := notifyFixture(t)
	hook := cronNotifyHook(svc)
	job := cron.Job{Name: "nightly"}

	hook(job, cron.StatusSucceeded, "")
	if events, _ := repo.ListEvents(context.Background(), notify.EventFilter{}); len(events) != 0 {
		t.Fatalf("a success with nothing open added %d events", len(events))
	}

	hook(job, cron.StatusFailed, "boom")
	hook(job, cron.StatusSucceeded, "")
	events, _ := repo.ListEvents(context.Background(), notify.EventFilter{})
	if len(events) != 2 || !events[0].Resolved {
		t.Fatalf("events after fail then success = %+v; want the resolution", events)
	}
	if got := deliveryStates(t, repo, ch.ID); len(got) != 2 || got[1] != "queued" {
		t.Fatalf("deliveries = %v, want failure then resolution queued", got)
	}
}

// The renewal built-in is reported as tls.renew_failed, never as cron.failed.
func TestCronHookSkipsRenewalBuiltin(t *testing.T) {
	svc, repo, _ := notifyFixture(t)
	cronNotifyHook(svc)(cron.Job{Name: "ssl-renew", Builtin: cron.BuiltinSSLRenew}, cron.StatusFailed, "x")
	if events, _ := repo.ListEvents(context.Background(), notify.EventFilter{}); len(events) != 0 {
		t.Fatalf("renewal built-in raised %d events through the cron hook", len(events))
	}
}

// A job failure is reported, and a later success of its kind clears it.
func TestJobsHookFailureThenSuccess(t *testing.T) {
	svc, repo, _ := notifyFixture(t)
	hook := jobsNotifyHook(svc)

	hook(jobs.Job{Kind: "app.install", State: jobs.StateFailed, Err: "apt exited 100"})
	hook(jobs.Job{Kind: "app.install", State: jobs.StateSucceeded})
	events, err := repo.ListEvents(context.Background(), notify.EventFilter{})
	if err != nil || len(events) != 2 || events[0].Kind != notify.KindJobFailed || !events[0].Resolved {
		t.Fatalf("job events = %+v, %v", events, err)
	}

	// Canceled and interrupted jobs report nothing.
	hook(jobs.Job{Kind: "x", State: jobs.StateCanceled})
	hook(jobs.Job{Kind: "x", State: jobs.StateInterrupted})
	if again, _ := repo.ListEvents(context.Background(), notify.EventFilter{}); len(again) != 2 {
		t.Fatalf("canceled or interrupted job raised events: %d", len(again))
	}
}

// A firewall-off condition opens on one sweep and resolves on the next, once it clears.
func TestSweepOpensAndResolves(t *testing.T) {
	svc, repo, _ := notifyFixture(t)
	ctx := context.Background()
	var out bytes.Buffer

	enabled := false
	src := sweepSource{firewall: func(context.Context) (bool, error) { return enabled, nil }}

	if err := sweepOnce(ctx, svc, src, &out, time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if s, _ := repo.Summary(ctx); s.OpenWarning != 1 {
		t.Fatalf("firewall off: open warnings = %d, want 1", s.OpenWarning)
	}

	enabled = true
	if err := sweepOnce(ctx, svc, src, &out, time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if s, _ := repo.Summary(ctx); s.OpenWarning != 0 {
		t.Fatalf("firewall back on: open warnings = %d, want 0", s.OpenWarning)
	}
}

// A source that fails aborts the sweep before any Sync, so nothing open is resolved.
func TestSweepSourceErrorResolvesNothing(t *testing.T) {
	svc, repo, _ := notifyFixture(t)
	ctx := context.Background()
	var out bytes.Buffer

	off := sweepSource{firewall: func(context.Context) (bool, error) { return false, nil }}
	if err := sweepOnce(ctx, svc, off, &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	failing := sweepSource{firewall: func(context.Context) (bool, error) { return false, io.ErrUnexpectedEOF }}
	if err := sweepOnce(ctx, svc, failing, &out, time.Now()); err == nil {
		t.Fatal("a failing source did not fail the sweep")
	}
	if s, _ := repo.Summary(ctx); s.OpenWarning != 1 {
		t.Fatalf("a read error resolved an open condition: open = %d", s.OpenWarning)
	}
}

// A source the host does not have leaves its kind out, so its open conditions survive.
func TestSweepMissingSourceKeepsItsKinds(t *testing.T) {
	svc, repo, _ := notifyFixture(t)
	ctx := context.Background()
	var out bytes.Buffer

	on := sweepSource{firewall: func(context.Context) (bool, error) { return false, nil }}
	if err := sweepOnce(ctx, svc, on, &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	// No firewall on this host: firewall.disabled is not synced at all.
	if err := sweepOnce(ctx, svc, sweepSource{}, &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	if s, _ := repo.Summary(ctx); s.OpenWarning != 1 {
		t.Fatalf("a host without the firewall source resolved its condition: open = %d", s.OpenWarning)
	}
}

// An unbound sweep fails, so the missing wiring shows up as a failed job.
func TestUnboundSweepFails(t *testing.T) {
	var n notifySweep
	if err := n.run(context.Background(), io.Discard); err == nil {
		t.Fatal("unbound sweep reported success")
	}
}
