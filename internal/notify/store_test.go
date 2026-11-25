package notify

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/secretbox"
	"github.com/portbyte-id/server-panel/internal/store"
)

var storeNow = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func openRepo(t *testing.T, withKey bool) (*Repo, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var box *secretbox.Box
	if withKey {
		key := make([]byte, secretbox.MasterKeySize)
		for i := range key {
			key[i] = 7
		}
		box, err = secretbox.New(key)
		if err != nil {
			t.Fatalf("secretbox.New: %v", err)
		}
	}
	return NewRepo(st, box, func() time.Time { return storeNow }), st
}

func webhookWrite(name string) ChannelWrite {
	return ChannelWrite{
		Name:         name,
		Type:         TypeWebhook,
		Enabled:      true,
		MinSeverity:  Warning,
		SendResolved: true,
		Config:       map[string]any{"url": "https://hooks.example.com/x", "sign": true},
		Secret:       map[string]string{"hmac_secret": "hunter2-hmac"},
	}
}

// openChannelFor creates one webhook channel for tests that need a delivery target.
func openChannelFor(t *testing.T, r *Repo, name string) Channel {
	t.Helper()
	c, err := r.CreateChannel(context.Background(), webhookWrite(name))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// insertTestEvent stores one normalized event and returns its id.
func insertTestEvent(t *testing.T, r *Repo, kind string, at time.Time) int64 {
	t.Helper()
	e, err := Normalize(Event{Kind: kind, Subject: "subject " + kind, Source: "test", At: at}, storeNow)
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.insertEvent(context.Background(), r.st.DB(), e)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMigrationCreatesNotifyTables(t *testing.T) {
	_, st := openRepo(t, true)
	for _, table := range []string{"notify_channels", "notify_events", "notify_state", "notify_deliveries"} {
		var n int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %s missing after migrations", table)
		}
	}
}

func TestSealedSecretRoundTripAndAAD(t *testing.T) {
	r, st := openRepo(t, true)
	ctx := context.Background()

	a := openChannelFor(t, r, "a")
	b := openChannelFor(t, r, "b")

	got, err := r.OpenSecret(ctx, a.ID)
	if err != nil || got["hmac_secret"] != "hunter2-hmac" {
		t.Fatalf("OpenSecret = %v, %v", got, err)
	}

	// The stored value must be sealed, not the plain secret.
	var blob string
	if err := st.DB().QueryRow(`SELECT secret FROM notify_channels WHERE id = ?`, a.ID).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(blob, "v1:") || strings.Contains(blob, "hunter2") {
		t.Fatalf("stored secret is not sealed: %q", blob)
	}

	// Copy a's sealed value onto b: the aad binds it to row a, so it must not open.
	if _, err := st.DB().Exec(`UPDATE notify_channels SET secret = ? WHERE id = ?`, blob, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.OpenSecret(ctx, b.ID); err == nil {
		t.Fatal("a secret copied to another row opened; aad is not enforced")
	}
}

func TestChannelJSONNeverCarriesSecret(t *testing.T) {
	r, _ := openRepo(t, true)
	c := openChannelFor(t, r, "api")
	if !c.SecretSet["hmac_secret"] {
		t.Errorf("SecretSet = %v, want hmac_secret set", c.SecretSet)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "hunter2") {
		t.Fatalf("API shape leaks the secret: %s", b)
	}
}

func TestCreateNeedsSecretKey(t *testing.T) {
	r, _ := openRepo(t, false)
	ctx := context.Background()
	if _, err := r.CreateChannel(ctx, webhookWrite("nokey")); !errors.Is(err, ErrNoSecretKey) {
		t.Fatalf("CreateChannel without a key = %v, want ErrNoSecretKey", err)
	}
	// Reads still work on a bell-only host.
	if _, err := r.ListChannels(ctx); err != nil {
		t.Fatalf("ListChannels on a keyless host: %v", err)
	}
}

func TestUnreadableSecretIsReportedNotFatal(t *testing.T) {
	r, _ := openRepo(t, true)
	ctx := context.Background()
	c := openChannelFor(t, r, "rot")

	// A host without the key (or with a rotated one) reads the same rows.
	keyless := NewRepo(r.st, nil, nil)
	got, err := keyless.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetChannel on a keyless host: %v", err)
	}
	if !got.SecretUnreadable {
		t.Errorf("SecretUnreadable = false, want true")
	}
}

func TestUpdateKeepsOmittedSecretAndClearsEmpty(t *testing.T) {
	r, _ := openRepo(t, true)
	ctx := context.Background()
	c := openChannelFor(t, r, "upd")

	// Name and config change, secret omitted: the stored secret stays.
	w := webhookWrite("upd2")
	w.Secret = nil
	if _, err := r.UpdateChannel(ctx, c.ID, w); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	got, err := r.OpenSecret(ctx, c.ID)
	if err != nil || got["hmac_secret"] != "hunter2-hmac" {
		t.Fatalf("omitted secret was lost: %v, %v", got, err)
	}

	// A field given "" is removed.
	w.Secret = map[string]string{"hmac_secret": ""}
	if _, err := r.UpdateChannel(ctx, c.ID, w); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	got, err = r.OpenSecret(ctx, c.ID)
	if err != nil || len(got) != 0 {
		t.Fatalf("cleared secret still present: %v, %v", got, err)
	}
}

func TestChannelNameUnique(t *testing.T) {
	r, _ := openRepo(t, true)
	openChannelFor(t, r, "dup")
	if _, err := r.CreateChannel(context.Background(), webhookWrite("dup")); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name = %v, want ErrNameTaken", err)
	}
}

func TestDeleteChannelRemovesItsDeliveries(t *testing.T) {
	r, _ := openRepo(t, true)
	ctx := context.Background()
	c := openChannelFor(t, r, "gone")
	evID := insertTestEvent(t, r, "tls.expiring", storeNow)
	if _, err := r.insertDelivery(ctx, r.st.DB(), delivery{
		EventID: evID, ChannelID: c.ID, State: "queued", CreatedAt: storeNow,
	}); err != nil {
		t.Fatal(err)
	}

	if err := r.DeleteChannel(ctx, c.ID); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	rows, err := r.ListDeliveries(ctx, DeliveryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("deliveries of a deleted channel remain: %+v", rows)
	}
	if err := r.DeleteChannel(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

func TestValidateWriteRefusals(t *testing.T) {
	bad := []func(*ChannelWrite){
		func(w *ChannelWrite) { w.Name = "  " },
		func(w *ChannelWrite) { w.Name = strings.Repeat("n", 65) },
		func(w *ChannelWrite) { w.Type = "sms" },
		func(w *ChannelWrite) { w.MinSeverity = "urgent" },
		func(w *ChannelWrite) { w.Kinds = []string{"not a kind"} },
		func(w *ChannelWrite) { w.QuietStart = "22:00" },
		func(w *ChannelWrite) { w.QuietStart, w.QuietEnd = "24:00", "07:00" },
	}
	for i, mutate := range bad {
		w := webhookWrite("v")
		mutate(&w)
		if _, err := validateWrite(w); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("case %d: err = %v, want ErrInvalidEvent", i, err)
		}
	}
	w, err := validateWrite(ChannelWrite{Name: "ok", Type: TypeSMTP})
	if err != nil {
		t.Fatalf("minimal channel refused: %v", err)
	}
	if w.MinSeverity != Warning {
		t.Errorf("default min severity = %q, want warning (D9)", w.MinSeverity)
	}
}

func TestClockMinutes(t *testing.T) {
	cases := map[string]int{"00:00": 0, "07:00": 420, "23:59": 1439}
	for in, want := range cases {
		got, err := clockMinutes(in)
		if err != nil || got != want {
			t.Errorf("clockMinutes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"7:00", "24:00", "12:60", "ab:cd", "1200", ""} {
		if _, err := clockMinutes(in); err == nil {
			t.Errorf("clockMinutes(%q) accepted", in)
		}
	}
}

func TestEventListReadAndSummary(t *testing.T) {
	r, _ := openRepo(t, true)
	ctx := context.Background()

	id1 := insertTestEvent(t, r, KindTLSRenewFailed, storeNow.Add(-2*time.Minute))
	id2 := insertTestEvent(t, r, KindCronFailed, storeNow.Add(-time.Minute))
	id3 := insertTestEvent(t, r, KindBanAdded, storeNow)

	list, err := r.ListEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].ID != id3 || list[2].ID != id1 {
		t.Fatalf("ListEvents newest-first = %+v", list)
	}

	n, err := r.MarkRead(ctx, []int64{id1})
	if err != nil || n != 1 {
		t.Fatalf("MarkRead = %d, %v", n, err)
	}
	unread, err := r.ListEvents(ctx, EventFilter{UnreadOnly: true})
	if err != nil || len(unread) != 2 {
		t.Fatalf("unread list = %d, %v", len(unread), err)
	}
	// Marking again changes nothing.
	if n, _ := r.MarkRead(ctx, []int64{id1}); n != 0 {
		t.Errorf("second MarkRead changed %d rows", n)
	}

	s, err := r.Summary(ctx)
	if err != nil || s.Unread != 2 {
		t.Fatalf("Summary = %+v, %v", s, err)
	}
	if n, err := r.MarkAllRead(ctx); err != nil || n != 2 {
		t.Fatalf("MarkAllRead = %d, %v", n, err)
	}
	page, err := r.ListEvents(ctx, EventFilter{Limit: 1, BeforeID: id3})
	if err != nil || len(page) != 1 || page[0].ID != id2 {
		t.Fatalf("paged list = %+v, %v", page, err)
	}
}

func TestSummaryCountsOpenKeysBySeverity(t *testing.T) {
	r, _ := openRepo(t, true)
	ctx := context.Background()
	states := []stateRow{
		{Key: "a", Kind: KindTLSRenewFailed, Source: "tls", Open: true, Severity: Critical, OpenedAt: storeNow},
		{Key: "b", Kind: KindCronFailed, Source: "cron", Open: true, Severity: Warning, OpenedAt: storeNow},
		{Key: "c", Kind: KindCronFailed, Source: "cron", Open: false, Severity: Warning, OpenedAt: storeNow},
	}
	for _, s := range states {
		if err := r.putState(ctx, r.st.DB(), s); err != nil {
			t.Fatal(err)
		}
	}
	s, err := r.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.OpenCritical != 1 || s.OpenWarning != 1 {
		t.Fatalf("Summary = %+v, want 1 critical and 1 warning open", s)
	}
}

func TestPruneKeepsNewestAndDropsOldWithDeliveries(t *testing.T) {
	r, _ := openRepo(t, true)
	ctx := context.Background()
	c := openChannelFor(t, r, "prune")

	var ids []int64
	for i := range 5 {
		// Event 0 is older than the age limit; the rest are recent.
		at := storeNow.Add(-40 * 24 * time.Hour)
		if i > 0 {
			at = storeNow.Add(-time.Duration(5-i) * time.Hour)
		}
		id := insertTestEvent(t, r, KindCronFailed, at)
		ids = append(ids, id)
		if _, err := r.insertDelivery(ctx, r.st.DB(), delivery{
			EventID: id, ChannelID: c.ID, State: "sent", CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := r.pruneEvents(ctx, 3, 30*24*time.Hour, storeNow)
	if err != nil {
		t.Fatalf("pruneEvents: %v", err)
	}
	// ids[0] is too old; ids[1] falls outside the newest three.
	if removed != 2 {
		t.Fatalf("removed %d, want 2", removed)
	}
	left, err := r.ListEvents(ctx, EventFilter{})
	if err != nil || len(left) != 3 || left[2].ID != ids[2] {
		t.Fatalf("kept events = %+v, %v", left, err)
	}
	rows, err := r.ListDeliveries(ctx, DeliveryFilter{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("deliveries after prune = %d, %v; want 3 (dropped events' rows go with them)", len(rows), err)
	}
}

func TestOpenStatesAreScopedBySourceAndKind(t *testing.T) {
	r, _ := openRepo(t, true)
	ctx := context.Background()
	for _, s := range []stateRow{
		{Key: "tls.expiring:a.example", Kind: KindTLSExpiring, Source: "sweep", Open: true, Severity: Warning, OpenedAt: storeNow},
		{Key: "firewall.disabled", Kind: KindFirewallDisabled, Source: "sweep", Open: true, Severity: Warning, OpenedAt: storeNow},
		{Key: "cron.failed:x", Kind: KindCronFailed, Source: "cron", Open: true, Severity: Warning, OpenedAt: storeNow},
	} {
		if err := r.putState(ctx, r.st.DB(), s); err != nil {
			t.Fatal(err)
		}
	}

	got, err := r.openStates(ctx, r.st.DB(), "sweep", []string{KindTLSExpiring})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "tls.expiring:a.example" {
		t.Fatalf("openStates(sweep, tls.expiring) = %+v", got)
	}
	if got, _ := r.openStates(ctx, r.st.DB(), "sweep", nil); len(got) != 0 {
		t.Errorf("empty kinds returned %d rows", len(got))
	}
}

func TestStateRowRoundTrip(t *testing.T) {
	r, _ := openRepo(t, true)
	ctx := context.Background()
	want := stateRow{Key: "k", Kind: KindCronFailed, Source: "cron", Open: true, Severity: Warning,
		OpenedAt: storeNow, LastEventID: 9}
	if err := r.putState(ctx, r.st.DB(), want); err != nil {
		t.Fatal(err)
	}
	got, found, err := r.getState(ctx, r.st.DB(), "k")
	if err != nil || !found || got.Key != want.Key || !got.Open || got.LastEventID != 9 || !got.OpenedAt.Equal(storeNow) {
		t.Fatalf("getState = %+v, %v, %v", got, found, err)
	}
	// Closing the key is an upsert on the same row.
	want.Open = false
	if err := r.putState(ctx, r.st.DB(), want); err != nil {
		t.Fatal(err)
	}
	got, _, _ = r.getState(ctx, r.st.DB(), "k")
	if got.Open {
		t.Errorf("state still open after close")
	}
	if _, found, _ := r.getState(ctx, r.st.DB(), "missing"); found {
		t.Errorf("missing key reported as found")
	}
}

func TestDeliveryQueueRetryAndLog(t *testing.T) {
	r, _ := openRepo(t, true)
	ctx := context.Background()
	c := openChannelFor(t, r, "queue")
	evID := insertTestEvent(t, r, KindCronFailed, storeNow)

	id, err := r.insertDelivery(ctx, r.st.DB(), delivery{EventID: evID, ChannelID: c.ID, State: "queued", CreatedAt: storeNow})
	if err != nil {
		t.Fatal(err)
	}
	due, err := r.dueDeliveries(ctx, storeNow, 10)
	if err != nil || len(due) != 1 || due[0].ID != id {
		t.Fatalf("dueDeliveries = %+v, %v", due, err)
	}

	later := storeNow.Add(time.Hour)
	d := due[0]
	d.State, d.Attempts, d.LastError, d.NextAt = "failed", 5, "boom", &later
	if err := r.updateDelivery(ctx, r.st.DB(), d); err != nil {
		t.Fatal(err)
	}
	if left, _ := r.dueDeliveries(ctx, storeNow, 10); len(left) != 0 {
		t.Fatalf("failed delivery still due")
	}

	if err := r.RetryDelivery(ctx, id); err != nil {
		t.Fatalf("RetryDelivery on failed: %v", err)
	}
	rows, err := r.ListDeliveries(ctx, DeliveryFilter{State: "queued"})
	if err != nil || len(rows) != 1 || rows[0].Attempts != 0 || rows[0].ChannelName != c.Name {
		t.Fatalf("after retry: %+v, %v", rows, err)
	}
	if err := r.RetryDelivery(ctx, id); !errors.Is(err, ErrInvalidState) {
		t.Errorf("retry of a queued row = %v, want ErrInvalidState", err)
	}
	if err := r.RetryDelivery(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("retry of a missing row = %v, want ErrNotFound", err)
	}
}
