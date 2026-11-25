package metrics

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/notify"
	"github.com/portbyte-id/server-panel/internal/store"
)

type recEmitter struct{ events []notify.Event }

func (r *recEmitter) Emit(_ context.Context, e notify.Event) { r.events = append(r.events, e) }

type fakeObserver struct{ obs Observations }

func (f *fakeObserver) Observe(context.Context, time.Time) (Observations, error) { return f.obs, nil }

func (f *fakeObserver) set(kind, target string, v float64) {
	f.obs = Observations{kind: {target: v}}
}

// alertEnv is the evaluator over a real panel.db with every built-in rule
// switched off, so a test sees only the rules it creates.
type alertEnv struct {
	t     *testing.T
	st    *store.Store
	rules *RuleStore
	obs   *fakeObserver
	emit  *recEmitter
	clk   *testClock
	a     *Alerts
}

func newAlertEnv(t *testing.T) *alertEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &alertEnv{t: t, st: st, rules: NewRuleStore(st), obs: &fakeObserver{}, emit: &recEmitter{}, clk: &testClock{t: base}}
	ctx := context.Background()
	list, err := e.rules.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		r.Enabled = false
		if _, err := e.rules.Update(ctx, r.ID, r); err != nil {
			t.Fatal(err)
		}
	}
	e.a = e.newAlerts()
	return e
}

func (e *alertEnv) newAlerts() *Alerts {
	a, err := NewAlerts(AlertsOptions{Rules: e.rules, Observe: e.obs, Emit: e.emit, Now: e.clk.now})
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *alertEnv) add(r Rule) Rule {
	e.t.Helper()
	r.Enabled = true
	if r.Severity == "" {
		r.Severity = "warning"
	}
	out, err := e.a.Create(context.Background(), r)
	if err != nil {
		e.t.Fatalf("create rule: %v", err)
	}
	return out
}

// at evaluates with value v at offset seconds from base.
func (e *alertEnv) at(offset int, kind, target string, v float64) {
	e.t.Helper()
	e.clk.t = base.Add(time.Duration(offset) * time.Second)
	e.obs.set(kind, target, v)
	if err := e.a.Evaluate(context.Background()); err != nil {
		e.t.Fatalf("evaluate at +%ds: %v", offset, err)
	}
}

func (e *alertEnv) stateOf(ruleID int64, target string) string {
	e.t.Helper()
	states, err := e.rules.States(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	for _, s := range states {
		if s.RuleID == ruleID && s.Target == target {
			return s.State
		}
	}
	return "ok"
}

func diskRule() Rule {
	return Rule{Kind: RuleDiskPct, Target: "*", Threshold: 85, ClearThreshold: 80, ForSeconds: 300, ClearForSeconds: 300}
}

func TestFiresOnlyAfterFor(t *testing.T) {
	e := newAlertEnv(t)
	r := e.add(diskRule())
	e.at(0, RuleDiskPct, "/", 90)
	if st := e.stateOf(r.ID, "/"); st != statePending || len(e.emit.events) != 0 {
		t.Fatalf("after the first breach: state %s, %d events", st, len(e.emit.events))
	}
	e.at(240, RuleDiskPct, "/", 91)
	if len(e.emit.events) != 0 {
		t.Fatalf("fired after 240 s of a 300 s rule")
	}
	e.at(300, RuleDiskPct, "/", 92)
	if len(e.emit.events) != 1 {
		t.Fatalf("events after 300 s = %d, want 1", len(e.emit.events))
	}
	ev := e.emit.events[0]
	if ev.Kind != notify.KindMetricsThreshold || ev.Resolved || ev.Severity != notify.Warning || ev.Source != "metrics" ||
		ev.Key != "metrics.threshold:"+itoa(r.ID)+":/" || !strings.Contains(ev.Subject, "Disk / at 92%") || ev.Link != "/" {
		t.Errorf("event = %+v", ev)
	}
	e.at(360, RuleDiskPct, "/", 93)
	e.at(900, RuleDiskPct, "/", 94)
	if len(e.emit.events) != 1 {
		t.Errorf("a condition that stays open emitted again: %d events", len(e.emit.events))
	}
}

func itoa(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{digits[n%10]}, b...)
	}
	return string(b)
}

func TestClearsOnlyAfterClearFor(t *testing.T) {
	e := newAlertEnv(t)
	r := e.add(diskRule())
	e.at(0, RuleDiskPct, "/", 90)
	e.at(300, RuleDiskPct, "/", 90) // fires
	e.at(400, RuleDiskPct, "/", 70) // clearing begins
	if st := e.stateOf(r.ID, "/"); st != stateClearing || len(e.emit.events) != 1 {
		t.Fatalf("state %s, %d events after the value fell", st, len(e.emit.events))
	}
	e.at(699, RuleDiskPct, "/", 70)
	if len(e.emit.events) != 1 {
		t.Fatalf("resolved after 299 s of a 300 s clear")
	}
	e.at(700, RuleDiskPct, "/", 70)
	if len(e.emit.events) != 2 || !e.emit.events[1].Resolved || e.emit.events[1].Key != e.emit.events[0].Key {
		t.Fatalf("events = %+v", e.emit.events)
	}
	if st := e.stateOf(r.ID, "/"); st != "ok" {
		t.Errorf("state after resolving = %s", st)
	}
}

func TestFlappingEmitsOneFireAndOneResolve(t *testing.T) {
	e := newAlertEnv(t)
	r := e.add(diskRule())
	// Hovering around the line before it has held: never reaches `for`.
	for i, v := range []float64{90, 70, 90, 70, 90, 70} {
		e.at(i*60, RuleDiskPct, "/", v)
	}
	if len(e.emit.events) != 0 {
		t.Fatalf("a flapping value fired: %+v", e.emit.events)
	}
	// Now it holds and fires, then flaps around the clear line.
	e.at(1000, RuleDiskPct, "/", 90)
	e.at(1300, RuleDiskPct, "/", 90)
	for i, v := range []float64{78, 84, 78, 84, 78} {
		e.at(1360+i*60, RuleDiskPct, "/", v)
	}
	if len(e.emit.events) != 1 {
		t.Fatalf("flapping around the clear line gave %d events, want only the fire", len(e.emit.events))
	}
	if st := e.stateOf(r.ID, "/"); st != stateClearing && st != stateFiring {
		t.Fatalf("state = %s", st)
	}
	e.at(2000, RuleDiskPct, "/", 60)
	e.at(2300, RuleDiskPct, "/", 60)
	if len(e.emit.events) != 2 || !e.emit.events[1].Resolved {
		t.Fatalf("events = %d, want one fire and one resolve", len(e.emit.events))
	}
}

func TestBetweenTheThresholdsNothingStarts(t *testing.T) {
	e := newAlertEnv(t)
	r := e.add(diskRule())
	for i := range 10 {
		e.at(i*60, RuleDiskPct, "/", 82) // under 85, above the clear line
	}
	if st := e.stateOf(r.ID, "/"); st != "ok" || len(e.emit.events) != 0 {
		t.Errorf("state %s, %d events for a value that never breached", st, len(e.emit.events))
	}
}

func TestZeroForFiresAtOnce(t *testing.T) {
	e := newAlertEnv(t)
	rule := diskRule()
	rule.ForSeconds, rule.ClearForSeconds = 0, 0
	e.add(rule)
	e.at(0, RuleDiskPct, "/", 90)
	e.at(60, RuleDiskPct, "/", 50)
	if len(e.emit.events) != 2 || e.emit.events[0].Resolved || !e.emit.events[1].Resolved {
		t.Errorf("events = %+v", e.emit.events)
	}
}

func TestRestartMidFiringDoesNotFireAgain(t *testing.T) {
	e := newAlertEnv(t)
	e.add(diskRule())
	e.at(0, RuleDiskPct, "/", 90)
	e.at(300, RuleDiskPct, "/", 90)
	if len(e.emit.events) != 1 {
		t.Fatalf("setup: %d events", len(e.emit.events))
	}
	e.a = e.newAlerts() // the daemon restarted; state is in panel.db
	e.at(360, RuleDiskPct, "/", 90)
	e.at(420, RuleDiskPct, "/", 90)
	if len(e.emit.events) != 1 {
		t.Errorf("a restart re-fired an open alert: %d events", len(e.emit.events))
	}
	// And the restart did not forget it: it resolves normally.
	e.at(500, RuleDiskPct, "/", 10)
	e.at(800, RuleDiskPct, "/", 10)
	if len(e.emit.events) != 2 || !e.emit.events[1].Resolved {
		t.Errorf("events = %d after the value recovered", len(e.emit.events))
	}
}

func TestDeletingAFiringRuleResolvesIt(t *testing.T) {
	e := newAlertEnv(t)
	r := e.add(diskRule())
	e.at(0, RuleDiskPct, "/", 90)
	e.at(300, RuleDiskPct, "/", 90)
	if err := e.a.Delete(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.emit.events) != 2 || !e.emit.events[1].Resolved {
		t.Fatalf("events = %+v, want a resolve for the deleted rule", e.emit.events)
	}
	if _, err := e.rules.Get(context.Background(), r.ID); !errors.Is(err, ErrRuleNotFound) {
		t.Errorf("rule still there: %v", err)
	}
	if states, _ := e.rules.States(context.Background()); len(states) != 0 {
		t.Errorf("state rows left behind: %+v", states)
	}
}

func TestDisablingAFiringRuleResolvesIt(t *testing.T) {
	e := newAlertEnv(t)
	r := e.add(diskRule())
	e.at(0, RuleDiskPct, "/", 90)
	e.at(300, RuleDiskPct, "/", 90)
	r.Enabled = false
	if _, err := e.a.Update(context.Background(), r.ID, r); err != nil {
		t.Fatal(err)
	}
	if len(e.emit.events) != 2 || !e.emit.events[1].Resolved {
		t.Fatalf("events = %+v", e.emit.events)
	}
	e.at(400, RuleDiskPct, "/", 95)
	e.at(900, RuleDiskPct, "/", 95)
	if len(e.emit.events) != 2 {
		t.Errorf("a disabled rule fired")
	}
}

func TestATargetThatVanishesResolves(t *testing.T) {
	e := newAlertEnv(t)
	r := e.add(Rule{Kind: RuleServiceDown, Target: "*", Threshold: 100, ClearThreshold: 0, ForSeconds: 0, ClearForSeconds: 60})
	e.at(0, RuleServiceDown, "app:blog", 100)
	if len(e.emit.events) != 1 || !strings.Contains(e.emit.events[0].Subject, "app:blog is down") {
		t.Fatalf("events = %+v", e.emit.events)
	}
	e.obs.obs = Observations{} // the app was deleted: no data for it any more
	e.clk.t = base.Add(time.Minute)
	if err := e.a.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.emit.events) != 2 || !e.emit.events[1].Resolved || e.stateOf(r.ID, "app:blog") != "ok" {
		t.Errorf("events = %+v", e.emit.events)
	}
}

func TestCertDaysCountDown(t *testing.T) {
	e := newAlertEnv(t)
	e.add(Rule{Kind: RuleCertDays, Target: "*", Threshold: 14, ClearThreshold: 21, ForSeconds: 0, ClearForSeconds: 0, Severity: "critical"})
	e.at(0, RuleCertDays, "example.com", 30) // plenty of time
	if len(e.emit.events) != 0 {
		t.Fatal("fired with 30 days left")
	}
	e.at(60, RuleCertDays, "example.com", 10)
	if len(e.emit.events) != 1 || e.emit.events[0].Severity != notify.Critical ||
		e.emit.events[0].Link != "/sites/example.com?section=ssl" {
		t.Fatalf("events = %+v", e.emit.events)
	}
	e.at(120, RuleCertDays, "example.com", 17) // between the lines: still open
	if len(e.emit.events) != 1 {
		t.Fatal("resolved inside the hysteresis band")
	}
	e.at(180, RuleCertDays, "example.com", 89) // renewed
	if len(e.emit.events) != 2 || !e.emit.events[1].Resolved {
		t.Fatalf("a renewed certificate did not resolve: %+v", e.emit.events)
	}
}

func TestWildcardTargetsAreIndependent(t *testing.T) {
	e := newAlertEnv(t)
	e.add(Rule{Kind: RuleDiskPct, Target: "*", Threshold: 85, ClearThreshold: 80, ForSeconds: 0, ClearForSeconds: 0})
	e.obs.obs = Observations{RuleDiskPct: {"/": 90, "/data": 40, "/var": 99}}
	if err := e.a.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.emit.events) != 2 {
		t.Fatalf("events = %+v, want / and /var", e.emit.events)
	}
	if e.emit.events[0].Key == e.emit.events[1].Key {
		t.Error("two mounts share one dedupe key")
	}
	// A rule with a named target ignores the others.
	e2 := newAlertEnv(t)
	e2.add(Rule{Kind: RuleDiskPct, Target: "/var", Threshold: 85, ClearThreshold: 80, ForSeconds: 0, ClearForSeconds: 0})
	e2.obs.obs = Observations{RuleDiskPct: {"/": 90, "/var": 99}}
	_ = e2.a.Evaluate(context.Background())
	if len(e2.emit.events) != 1 || !strings.Contains(e2.emit.events[0].Subject, "/var") {
		t.Errorf("named target events = %+v", e2.emit.events)
	}
}

func TestHostWideRulesAlwaysTargetHost(t *testing.T) {
	e := newAlertEnv(t)
	r := e.add(Rule{Kind: RuleMemPct, Target: "whatever", Threshold: 90, ClearThreshold: 80, ForSeconds: 0, ClearForSeconds: 0})
	if r.Target != "host" {
		t.Fatalf("target = %q", r.Target)
	}
	e.at(0, RuleMemPct, "host", 95)
	if len(e.emit.events) != 1 || e.emit.events[0].Subject != "Memory at 95% (threshold 90%)" {
		t.Errorf("events = %+v", e.emit.events)
	}
}

func TestAlertsSwitchedOffEvaluatesNothing(t *testing.T) {
	e := newAlertEnv(t)
	e.add(diskRule())
	off := false
	a, _ := NewAlerts(AlertsOptions{Rules: e.rules, Observe: e.obs, Emit: e.emit, Now: e.clk.now, Enabled: func() bool { return off }})
	e.obs.set(RuleDiskPct, "/", 99)
	if err := a.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if states, _ := e.rules.States(context.Background()); len(states) != 0 {
		t.Errorf("state written while alerts are off: %+v", states)
	}
}

func TestBuiltinRulesAreSeededAndProtected(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	rs := NewRuleStore(st)
	ctx := context.Background()
	list, err := rs.List(ctx)
	if err != nil || len(list) != 9 {
		t.Fatalf("seeded %d rules, %v; want 9", len(list), err)
	}
	var site5xx Rule
	for _, r := range list {
		if !r.Builtin {
			t.Errorf("rule %d is not builtin", r.ID)
		}
		if err := ValidateRule(r); err != nil {
			t.Errorf("seeded rule %d (%s) is invalid: %v", r.ID, r.Kind, err)
		}
		if r.Kind == RuleSite5xxPct {
			site5xx = r
		}
	}
	if site5xx.Enabled {
		t.Error("site_5xx_pct is seeded enabled; it must start off")
	}
	if err := rs.Delete(ctx, list[0].ID); !errors.Is(err, ErrBuiltinRule) {
		t.Errorf("deleting a built-in rule = %v", err)
	}
	// A built-in rule keeps its kind and target through an update.
	edit := list[0]
	edit.Kind, edit.Target, edit.Threshold = RuleMemPct, "/x", 88
	got, err := rs.Update(ctx, list[0].ID, edit)
	if err != nil || got.Kind != RuleDiskPct || got.Target != "*" || got.Threshold != 88 {
		t.Errorf("update = %+v, %v", got, err)
	}
}

func TestValidateRule(t *testing.T) {
	good := diskRule()
	good.Severity = "warning"
	if err := ValidateRule(good); err != nil {
		t.Fatalf("good rule: %v", err)
	}
	cases := []struct {
		name  string
		edit  func(*Rule)
		field string
	}{
		{"unknown kind", func(r *Rule) { r.Kind = "bogus" }, "kind"},
		{"empty target", func(r *Rule) { r.Target = "" }, "target"},
		{"newline in target", func(r *Rule) { r.Target = "a\nb" }, "target"},
		{"threshold over 100%", func(r *Rule) { r.Threshold = 101 }, "threshold"},
		{"negative threshold", func(r *Rule) { r.Threshold = -1 }, "threshold"},
		{"clear above threshold", func(r *Rule) { r.ClearThreshold = 90 }, "clear_threshold"},
		{"bad severity", func(r *Rule) { r.Severity = "fatal" }, "severity"},
		{"for too long", func(r *Rule) { r.ForSeconds = 100000 }, "for_seconds"},
		{"negative clear_for", func(r *Rule) { r.ClearForSeconds = -1 }, "clear_for_seconds"},
		{"cert clear below threshold", func(r *Rule) { r.Kind, r.Threshold, r.ClearThreshold = RuleCertDays, 14, 7 }, "clear_threshold"},
	}
	for _, c := range cases {
		r := good
		c.edit(&r)
		var fe *FieldError
		if err := ValidateRule(r); !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%s: error %v, want field %s", c.name, err, c.field)
		}
	}
}

func TestObserverReadsTheNewestRows(t *testing.T) {
	r, clk, d := newTestRecorder(t, Options{})
	ctx := context.Background()
	// Disk at 40% and memory 1000/(1000+3000) = 25% from snapAt; give the services
	// a failed minute and a site a 5xx burst.
	for i := range 12 {
		s := snapAt(base.Add(time.Duration(i)*5*time.Second), 20)
		s.Memory.SwapTotalBytes, s.Memory.SwapUsedBytes = 1000, 250
		s.CPUCores = []float64{1, 2}
		s.Load.One = 3
		r.Observe(s)
	}
	r.ObserveServices(base, []ServiceSample{{Name: "nginx", State: StateRunning}, {Name: "docker", State: StateFailed}})
	r.ObserveServices(base.Add(30*time.Second), []ServiceSample{{Name: "nginx", State: StateRunning}, {Name: "docker", State: StateFailed}})
	src := &fakeSites{minutes: []SiteMinute{
		{Domain: "busy.example", Minute: base.Unix(), Req: 100, S5: 20},
		{Domain: "quiet.example", Minute: base.Unix(), Req: 10, S5: 10},
	}}
	r.o.Sites = src
	clk.t = base.Add(80 * time.Second)
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	certs := func(context.Context) ([]CertExpiry, error) {
		return []CertExpiry{{Domain: "example.com", NotAfter: clk.t.Add(10 * 24 * time.Hour)}}, nil
	}
	obs, err := DBObserver{DB: d, Recorder: r, Certs: certs}.Observe(ctx, clk.t)
	if err != nil {
		t.Fatal(err)
	}
	approx := func(kind, target string, want float64) {
		t.Helper()
		got, ok := obs[kind][target]
		if !ok || got < want-0.01 || got > want+0.01 {
			t.Errorf("%s/%s = %v (present %v), want %v", kind, target, got, ok, want)
		}
	}
	approx(RuleCPUPct, "host", 20)
	approx(RuleMemPct, "host", 25)
	approx(RuleSwapPct, "host", 25)
	approx(RuleLoadPerCore, "host", 1.5)
	approx(RuleDiskPct, "/", 40)
	approx(RuleServiceDown, "nginx", 0)
	approx(RuleServiceDown, "docker", 100)
	approx(RuleSite5xxPct, "busy.example", 20)
	approx(RuleCertDays, "example.com", 10)
	if _, ok := obs[RuleSite5xxPct]["quiet.example"]; ok {
		t.Error("a site with 10 requests was judged on its 5xx share")
	}

	// Stale data is no data.
	clk.t = base.Add(time.Hour)
	stale, err := DBObserver{DB: d, Recorder: r}.Observe(ctx, clk.t)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale[RuleDiskPct])+len(stale[RuleMemPct])+len(stale[RuleServiceDown]) != 0 {
		t.Errorf("rows an hour old were treated as current: %+v", stale)
	}
}

func TestObserverFallsBackToMemoryWhenTheFlushFails(t *testing.T) {
	r, clk, d := newTestRecorder(t, Options{})
	ctx := context.Background()
	s := snapAt(base, 10)
	s.Disks[0].UsedPercent = 97
	r.Observe(s)
	if _, err := d.SQL().Exec(`DROP TABLE host_10s`); err != nil {
		t.Fatal(err)
	}
	clk.t = base.Add(2 * time.Minute)
	if err := r.Flush(ctx); err == nil {
		t.Fatal("flush into a missing table succeeded")
	}
	// disk_1m is untouched and empty, so only the in-memory value can alert.
	obs, err := DBObserver{DB: d, Recorder: r}.Observe(ctx, clk.t)
	if err != nil {
		t.Fatal(err)
	}
	if got := obs[RuleDiskPct]["/"]; got != 97 {
		t.Errorf("disk from memory = %v, want 97", got)
	}
}
