package notify

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// fakeSettings answers the notify.* keys the engine reads.
type fakeSettings struct {
	enabled bool
	window  time.Duration
	perHour int
}

func (f *fakeSettings) Bool(_ context.Context, _ string) (bool, error) { return f.enabled, nil }
func (f *fakeSettings) Int(_ context.Context, _ string) (int, error)   { return f.perHour, nil }
func (f *fakeSettings) Duration(_ context.Context, _ string) (time.Duration, error) {
	return f.window, nil
}

// clock is a settable time source shared by the repo and the engine.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// engine builds a Service over a fresh store with a clock at t0.
func engine(t *testing.T, set *fakeSettings) (*Service, *Repo, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	r, _ := openRepo(t, true)
	r.now = clk.now
	if set == nil {
		set = &fakeSettings{enabled: true, window: 6 * time.Hour, perHour: 30}
	}
	svc := New(Options{
		Repo:     r,
		Settings: set,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      clk.now,
	})
	return svc, r, clk
}

// channelFor creates a webhook channel with the given overrides applied.
func channelFor(t *testing.T, r *Repo, name string, mutate func(*ChannelWrite)) Channel {
	t.Helper()
	w := webhookWrite(name)
	w.SendResolved = true
	if mutate != nil {
		mutate(&w)
	}
	c, err := r.CreateChannel(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// rows returns the delivery rows of one channel, oldest first.
func rows(t *testing.T, r *Repo, channelID int64) []DeliveryRow {
	t.Helper()
	all, err := r.ListDeliveries(context.Background(), DeliveryFilter{ChannelID: channelID, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]DeliveryRow, len(all))
	for i, d := range all {
		out[len(all)-1-i] = d
	}
	return out
}

func expectRows(t *testing.T, got []DeliveryRow, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d deliveries, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		state, reason := w, ""
		for j := range w {
			if w[j] == ':' {
				state, reason = w[:j], w[j+1:]
				break
			}
		}
		if got[i].State != state || got[i].Reason != reason {
			t.Errorf("delivery %d = %s/%q, want %s/%q", i, got[i].State, got[i].Reason, state, reason)
		}
	}
}

func warn(key string) Event {
	return Event{Kind: KindCronFailed, Key: key, Subject: "job failed", Source: "cron"}
}

func TestDedupeWithinAndOutsideWindow(t *testing.T) {
	svc, r, clk := engine(t, nil)
	ctx := context.Background()
	c := channelFor(t, r, "dedupe", nil)

	svc.Emit(ctx, warn("cron.failed:a"))
	clk.t = clk.t.Add(time.Hour)
	svc.Emit(ctx, warn("cron.failed:a"))
	clk.t = clk.t.Add(6 * time.Hour) // 7h after the first, outside the 6h window
	svc.Emit(ctx, warn("cron.failed:a"))

	expectRows(t, rows(t, r, c.ID), "queued", "suppressed:dedupe", "queued")
}

func TestEscalationResendsAtOnce(t *testing.T) {
	svc, r, clk := engine(t, nil)
	ctx := context.Background()
	c := channelFor(t, r, "esc", nil)

	svc.Emit(ctx, Event{Kind: KindTLSExpiring, Severity: Warning, Key: "tls.expiring:x", Subject: "s", Source: "tls"})
	clk.t = clk.t.Add(time.Minute)
	svc.Emit(ctx, Event{Kind: KindTLSExpiring, Severity: Critical, Key: "tls.expiring:x", Subject: "s", Source: "tls"})
	clk.t = clk.t.Add(time.Minute)
	svc.Emit(ctx, Event{Kind: KindTLSExpiring, Severity: Critical, Key: "tls.expiring:x", Subject: "s", Source: "tls"})

	expectRows(t, rows(t, r, c.ID), "queued", "queued", "suppressed:dedupe")
}

func TestResolvedSentOnceAndOnlyAfterAProblem(t *testing.T) {
	svc, r, _ := engine(t, nil)
	ctx := context.Background()
	c := channelFor(t, r, "res", nil)

	// A resolved event with no open problem goes nowhere.
	svc.Emit(ctx, Event{Kind: KindCronFailed, Key: "cron.failed:b", Subject: "ok", Source: "cron", Resolved: true})
	expectRows(t, rows(t, r, c.ID))

	svc.Emit(ctx, warn("cron.failed:b"))
	svc.Emit(ctx, Event{Kind: KindCronFailed, Key: "cron.failed:b", Subject: "ok", Source: "cron", Resolved: true})
	svc.Emit(ctx, Event{Kind: KindCronFailed, Key: "cron.failed:b", Subject: "ok", Source: "cron", Resolved: true})

	expectRows(t, rows(t, r, c.ID), "queued", "queued")
}

func TestResolvedNotSentWhenChannelOptsOut(t *testing.T) {
	svc, r, _ := engine(t, nil)
	ctx := context.Background()
	c := channelFor(t, r, "nores", func(w *ChannelWrite) { w.SendResolved = false })

	svc.Emit(ctx, warn("cron.failed:c"))
	svc.Emit(ctx, Event{Kind: KindCronFailed, Key: "cron.failed:c", Subject: "ok", Source: "cron", Resolved: true})

	expectRows(t, rows(t, r, c.ID), "queued")
}

func TestQuietHoursAcrossMidnight(t *testing.T) {
	svc, r, clk := engine(t, nil)
	ctx := context.Background()
	c := channelFor(t, r, "quiet", func(w *ChannelWrite) {
		w.QuietStart, w.QuietEnd = "22:00", "07:00"
		w.MinSeverity = Info
	})

	at := func(h, m int) {
		clk.t = time.Date(2026, 11, 25, h, m, 0, 0, time.Local)
	}
	at(23, 0)
	svc.Emit(ctx, Event{Kind: KindCronFailed, Key: "k1", Subject: "s", Source: "cron"})
	at(6, 59)
	svc.Emit(ctx, Event{Kind: KindCronFailed, Key: "k2", Subject: "s", Source: "cron"})
	at(7, 0)
	svc.Emit(ctx, Event{Kind: KindCronFailed, Key: "k3", Subject: "s", Source: "cron"})
	at(23, 30)
	svc.Emit(ctx, Event{Kind: KindCronFailed, Key: "k4", Subject: "s", Source: "cron", Severity: Critical})

	expectRows(t, rows(t, r, c.ID), "suppressed:quiet", "suppressed:quiet", "queued", "queued")
}

func TestRateLimitAndRefill(t *testing.T) {
	svc, r, clk := engine(t, &fakeSettings{enabled: true, window: time.Hour, perHour: 3})
	ctx := context.Background()
	c := channelFor(t, r, "rate", nil)

	for _, key := range []string{"r1", "r2", "r3", "r4"} {
		svc.Emit(ctx, Event{Kind: KindCronFailed, Key: key, Subject: "s", Source: "cron", Severity: Critical})
	}
	expectRows(t, rows(t, r, c.ID), "queued", "queued", "queued", "suppressed:rate")

	// Three per hour refill one token every 20 minutes.
	clk.t = clk.t.Add(20 * time.Minute)
	svc.Emit(ctx, Event{Kind: KindCronFailed, Key: "r5", Subject: "s", Source: "cron", Severity: Critical})
	expectRows(t, rows(t, r, c.ID), "queued", "queued", "queued", "suppressed:rate", "queued")
}

func TestNotifyDisabledSuppressesButRecords(t *testing.T) {
	svc, r, _ := engine(t, &fakeSettings{enabled: false, window: time.Hour, perHour: 30})
	ctx := context.Background()
	c := channelFor(t, r, "off", nil)

	svc.Emit(ctx, warn("cron.failed:off"))
	expectRows(t, rows(t, r, c.ID), "suppressed:disabled")

	events, err := r.ListEvents(ctx, EventFilter{})
	if err != nil || len(events) != 1 {
		t.Fatalf("the bell must still get the event: %d, %v", len(events), err)
	}
}

func TestRoutingFilters(t *testing.T) {
	svc, r, _ := engine(t, nil)
	ctx := context.Background()
	critOnly := channelFor(t, r, "crit", func(w *ChannelWrite) { w.MinSeverity = Critical })
	onlyBackup := channelFor(t, r, "kinds", func(w *ChannelWrite) { w.Kinds = []string{KindBackupFailed} })
	disabled := channelFor(t, r, "disabled", func(w *ChannelWrite) { w.Enabled = false })

	svc.Emit(ctx, warn("cron.failed:f"))

	expectRows(t, rows(t, r, critOnly.ID))
	expectRows(t, rows(t, r, onlyBackup.ID))
	expectRows(t, rows(t, r, disabled.ID))
}

func TestSyncResolvesOnlyMissingOpenKeysOfItsSourceAndKinds(t *testing.T) {
	svc, r, _ := engine(t, nil)
	ctx := context.Background()
	c := channelFor(t, r, "sync", nil)

	svc.Sync(ctx, "sweep", []string{KindTLSExpiring, KindFirewallDisabled}, []Event{
		{Kind: KindTLSExpiring, Key: "tls.expiring:a", Subject: "a", Severity: Warning},
		{Kind: KindTLSExpiring, Key: "tls.expiring:b", Subject: "b", Severity: Warning},
	})
	svc.Emit(ctx, Event{Kind: KindFirewallDisabled, Key: "firewall.disabled", Subject: "fw", Source: "sweep"})

	// b clears; a stays; the firewall key is not in the kinds passed, so it stays open.
	svc.Sync(ctx, "sweep", []string{KindTLSExpiring}, []Event{
		{Kind: KindTLSExpiring, Key: "tls.expiring:a", Subject: "a", Severity: Warning},
	})

	open, err := r.openStates(ctx, r.st.DB(), "sweep", []string{KindTLSExpiring, KindFirewallDisabled})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, o := range open {
		keys[o.Key] = true
	}
	if !keys["tls.expiring:a"] || keys["tls.expiring:b"] || !keys["firewall.disabled"] {
		t.Fatalf("open keys after sync = %v", keys)
	}

	// Two problems for a and b, then b's resolved: the channel sees
	// queued (a), queued (b), queued (a is not resent; dedupe), resolved (b).
	got := rows(t, r, c.ID)
	last := got[len(got)-1]
	if last.State != stateQueued || last.Kind != KindTLSExpiring {
		t.Fatalf("last delivery = %+v, want a queued tls.expiring resolution", last)
	}
}

func TestEmitWithCanceledContextStillStores(t *testing.T) {
	svc, r, _ := engine(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	svc.Emit(ctx, warn("cron.failed:cancel"))

	events, err := r.ListEvents(context.Background(), EventFilter{})
	if err != nil || len(events) != 1 {
		t.Fatalf("event with a canceled ctx not stored: %d, %v", len(events), err)
	}
}

func TestEmitMalformedDroppedAndDatabaseFailureBounded(t *testing.T) {
	svc, r, _ := engine(t, nil)
	ctx := context.Background()

	svc.Emit(ctx, Event{Kind: "bad", Subject: "s", Source: "x"}) // malformed kind
	if events, _ := r.ListEvents(ctx, EventFilter{}); len(events) != 0 {
		t.Fatalf("malformed event stored")
	}

	if err := r.st.Close(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	svc.Emit(ctx, warn("cron.failed:closed")) // must not panic or hang
	if d := time.Since(start); d > emitBudget+500*time.Millisecond {
		t.Fatalf("Emit took %v with a broken database, budget is %v", d, emitBudget)
	}
}

func TestSubscribeDeliversAndUnsubscribeCloses(t *testing.T) {
	svc, _, _ := engine(t, nil)
	ctx := context.Background()
	ch, unsub := svc.Subscribe()

	svc.Emit(ctx, warn("cron.failed:sub"))
	select {
	case ev := <-ch:
		if ev.Kind != KindCronFailed || ev.ID == 0 {
			t.Fatalf("subscriber got %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event delivered to the subscriber")
	}

	unsub()
	unsub() // a second call must be harmless
	if _, open := <-ch; open {
		t.Fatal("channel still open after unsubscribe")
	}
}

func TestSubscriberThatFallsBehindNeverBlocksEmit(t *testing.T) {
	svc, _, _ := engine(t, nil)
	ctx := context.Background()
	_, unsub := svc.Subscribe() // never read
	defer unsub()

	done := make(chan struct{})
	go func() {
		for i := range subscriberBuffer * 2 {
			svc.Emit(ctx, warn("cron.failed:slow"+string(rune('a'+i%26))))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Emit blocked on a subscriber that does not read")
	}
}

func TestLimiterBucketRules(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var b bucket
	for i := range 2 {
		if !b.take(t0, 2) {
			t.Fatalf("take %d refused on a fresh bucket", i)
		}
	}
	if b.take(t0, 2) {
		t.Fatal("third take in the same instant allowed")
	}
	if !b.take(t0.Add(30*time.Minute), 2) {
		t.Fatal("half an hour at 2/h should refill one token")
	}
}

func TestInQuietBoundaries(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 3, 1, h, m, 0, 0, time.Local) }
	cases := []struct {
		start, end string
		h, m       int
		want       bool
	}{
		{"22:00", "07:00", 23, 0, true},
		{"22:00", "07:00", 6, 59, true},
		{"22:00", "07:00", 7, 0, false},
		{"22:00", "07:00", 21, 59, false},
		{"09:00", "17:00", 12, 0, true},
		{"09:00", "17:00", 17, 0, false},
		{"", "", 12, 0, false},
		{"10:00", "10:00", 10, 0, false},
	}
	for _, c := range cases {
		if got := inQuiet(c.start, c.end, at(c.h, c.m)); got != c.want {
			t.Errorf("inQuiet(%s-%s at %02d:%02d) = %v, want %v", c.start, c.end, c.h, c.m, got, c.want)
		}
	}
}
