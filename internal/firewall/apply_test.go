package firewall

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

// fakeTables simulates the slice of iptables the manager touches: the two owned
// chains and the INPUT jump, per family. It parses the same restore text the real
// tool would, so a rollback that restores the wrong thing shows up as wrong state.
type fakeTables struct {
	mu        sync.Mutex
	chains    map[string][]string // "iptables:PANEL-INPUT" -> -A lines
	exists    map[string]bool     // "iptables:PANEL-INPUT"
	hook      map[string]bool     // "iptables"
	rejectV6  bool                // --test fails for ip6tables
	failApply map[string]bool     // real restore fails for this restore binary
	restores  []string            // stdin of every real (non --test) restore
}

func newFake() *fakeTables {
	return &fakeTables{chains: map[string][]string{}, exists: map[string]bool{}, hook: map[string]bool{}, failApply: map[string]bool{}}
}

func binOf(tool string) string {
	tool = strings.TrimSuffix(tool, "-save")
	return strings.TrimSuffix(tool, "-restore")
}

func (f *fakeTables) Output(_ context.Context, name string, _ ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bin := binOf(name)
	var b strings.Builder
	b.WriteString("*filter\n:INPUT ACCEPT [0:0]\n:DOCKER-USER - [0:0]\n")
	for _, c := range []string{ChainInput, ChainBlock} {
		if f.exists[bin+":"+c] {
			b.WriteString(":" + c + " - [0:0]\n")
		}
	}
	b.WriteString("-A DOCKER-USER -j RETURN\n")
	for _, c := range []string{ChainInput, ChainBlock} {
		for _, l := range f.chains[bin+":"+c] {
			b.WriteString(l + "\n")
		}
	}
	b.WriteString("COMMIT\n")
	return []byte(b.String()), nil
}

func (f *fakeTables) RunStdin(_ context.Context, stdin string, name string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch name {
	case "iptables", "ip6tables":
		switch args[0] {
		case "-C":
			if f.hook[name] {
				return nil
			}
			return errors.New("no such rule")
		case "-D":
			f.hook[name] = false
		}
	case "iptables-restore", "ip6tables-restore":
		bin := binOf(name)
		if args[0] == "--test" {
			if bin == "ip6tables" && f.rejectV6 {
				return errors.New("ip6tables rejected")
			}
			return nil
		}
		if f.failApply[name] {
			delete(f.failApply, name) // fail the apply once, let the rollback through
			return errors.New("restore failed")
		}
		f.restores = append(f.restores, stdin)
		for l := range strings.SplitSeq(stdin, "\n") {
			ff := strings.Fields(l)
			if len(ff) == 0 {
				continue
			}
			switch {
			case strings.HasPrefix(l, ":"):
				f.exists[bin+":"+ff[0][1:]] = true
			case ff[0] == "-F":
				f.chains[bin+":"+ff[1]] = nil
			case ff[0] == "-A":
				k := bin + ":" + ff[1]
				f.chains[k] = append(f.chains[k], l)
			case ff[0] == "-I":
				f.hook[bin] = true
			}
		}
	}
	return nil
}

type timers struct{ fns []func() }

func (t *timers) after(_ time.Duration, fn func()) func() bool {
	t.fns = append(t.fns, fn)
	i := len(t.fns) - 1
	return func() bool { t.fns[i] = nil; return true }
}

func (t *timers) fireAll() {
	for _, fn := range t.fns {
		if fn != nil {
			fn()
		}
	}
}

type harness struct {
	fake *fakeTables
	tm   *timers
	st   *store.Store
	now  time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &harness{fake: newFake(), tm: &timers{}, st: st, now: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
}

func (h *harness) manager(t *testing.T, ipv6 bool) *Manager {
	t.Helper()
	m, err := NewManager(Options{
		Store: h.st, Cmd: h.fake, IPv6: ipv6, Window: time.Minute,
		Now: func() time.Time { return h.now }, After: h.tm.after,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func input(port int) RenderInput {
	return RenderInput{
		SSHPorts: []int{22}, PanelPort: 8443,
		Rules: []Rule{{PortStart: port, PortEnd: port, Protocol: ProtoTCP, Enabled: true}},
	}
}

func (h *harness) has(bin, port string) bool {
	for _, l := range h.fake.chains[bin+":"+ChainInput] {
		if strings.Contains(l, "--dport "+port+" ") {
			return true
		}
	}
	return false
}

func TestApplyConfirmKeeps(t *testing.T) {
	h := newHarness(t)
	m := h.manager(t, true)
	ctx := context.Background()

	p, err := m.Apply(ctx, input(80))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Deadline.Equal(h.now.Add(time.Minute)) {
		t.Fatalf("deadline %v", p.Deadline)
	}
	if !h.has("iptables", "80") || !h.has("ip6tables", "80") || !h.fake.hook["iptables"] || !h.fake.hook["ip6tables"] {
		t.Fatal("ruleset not live and hooked in both families")
	}
	if err := m.Confirm(ctx, p.Token); err != nil {
		t.Fatal(err)
	}
	h.tm.fireAll() // the timer was stopped; firing must change nothing
	if !h.has("iptables", "80") {
		t.Fatal("confirmed rules were rolled back")
	}
	if _, err := m.Pending(ctx); !errors.Is(err, ErrNoPending) {
		t.Fatalf("pending after confirm: %v", err)
	}
	var enabled int
	var v4 string
	if err := h.st.DB().QueryRow(`SELECT enabled, confirmed_v4 FROM fw_state WHERE id=1`).Scan(&enabled, &v4); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 || !strings.Contains(v4, "-I INPUT 1 -j PANEL-INPUT") {
		t.Fatalf("state not recorded with a hooking ruleset: enabled=%d", enabled)
	}
}

func TestTimeoutRestoresPrevious(t *testing.T) {
	h := newHarness(t)
	m := h.manager(t, true)
	ctx := context.Background()

	first, err := m.Apply(ctx, input(80))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Confirm(ctx, first.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Apply(ctx, input(9000)); err != nil {
		t.Fatal(err)
	}
	if !h.has("iptables", "9000") || h.has("iptables", "80") {
		t.Fatal("second ruleset not live")
	}
	h.tm.fireAll() // 60 s pass without a confirm
	if !h.has("iptables", "80") || h.has("iptables", "9000") || !h.has("ip6tables", "80") {
		t.Fatalf("previous rules not restored: %v", h.fake.chains)
	}
	if !h.fake.hook["iptables"] {
		t.Fatal("rollback removed a jump that existed before the apply")
	}
	if _, err := m.Pending(ctx); !errors.Is(err, ErrNoPending) {
		t.Fatal("pending row survived the rollback")
	}
}

func TestFirstEnableRollbackUnhooksAndEmpties(t *testing.T) {
	h := newHarness(t)
	m := h.manager(t, true)
	if _, err := m.Apply(context.Background(), input(80)); err != nil {
		t.Fatal(err)
	}
	h.tm.fireAll()
	if h.fake.hook["iptables"] || h.fake.hook["ip6tables"] {
		t.Fatal("jump left behind after rolling back a first enable")
	}
	if len(h.fake.chains["iptables:"+ChainInput]) != 0 {
		t.Fatal("owned chain not emptied")
	}
}

func TestSecondApplyDoesNotStackJump(t *testing.T) {
	h := newHarness(t)
	m := h.manager(t, false)
	ctx := context.Background()
	p, err := m.Apply(ctx, input(80))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Confirm(ctx, p.Token); err != nil {
		t.Fatal(err)
	}
	h.fake.restores = nil
	if _, err := m.Apply(ctx, input(81)); err != nil {
		t.Fatal(err)
	}
	if len(h.fake.restores) != 1 || strings.Contains(h.fake.restores[0], "-I INPUT") {
		t.Fatalf("second apply must not insert the jump again: %q", h.fake.restores)
	}
}

func TestOnlyOnePending(t *testing.T) {
	h := newHarness(t)
	m := h.manager(t, false)
	if _, err := m.Apply(context.Background(), input(80)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Apply(context.Background(), input(81)); !errors.Is(err, ErrPending) {
		t.Fatalf("got %v", err)
	}
}

func TestConfirmWrongTokenAndNone(t *testing.T) {
	h := newHarness(t)
	m := h.manager(t, false)
	ctx := context.Background()
	if err := m.Confirm(ctx, "x"); !errors.Is(err, ErrNoPending) {
		t.Fatalf("got %v", err)
	}
	if _, err := m.Apply(ctx, input(80)); err != nil {
		t.Fatal(err)
	}
	if err := m.Confirm(ctx, "wrong"); !errors.Is(err, ErrToken) {
		t.Fatalf("got %v", err)
	}
	if _, err := m.Pending(ctx); err != nil {
		t.Fatal("a wrong token must not clear the pending apply")
	}
}

func TestConfirmAfterDeadlineRollsBack(t *testing.T) {
	h := newHarness(t)
	m := h.manager(t, false)
	ctx := context.Background()
	p, err := m.Apply(ctx, input(80))
	if err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(61 * time.Second) // timer has not fired yet, clock is past the deadline
	if err := m.Confirm(ctx, p.Token); !errors.Is(err, ErrExpired) {
		t.Fatalf("got %v", err)
	}
	if h.has("iptables", "80") || h.fake.hook["iptables"] {
		t.Fatal("late confirm kept the rules")
	}
}

func TestRestartWithPendingRollsBack(t *testing.T) {
	h := newHarness(t)
	m := h.manager(t, true)
	ctx := context.Background()
	if _, err := m.Apply(ctx, input(80)); err != nil {
		t.Fatal(err)
	}
	// Daemon dies: the timer is gone, the pending row and live rules remain.
	h.tm = &timers{}
	m2 := h.manager(t, true)
	if err := m2.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if h.has("iptables", "80") || h.fake.hook["iptables"] || h.fake.hook["ip6tables"] {
		t.Fatal("restart did not roll back the unconfirmed apply")
	}
	if err := m2.Recover(ctx); err != nil {
		t.Fatalf("second Recover must be a no-op: %v", err)
	}
}

func TestRevertNow(t *testing.T) {
	h := newHarness(t)
	m := h.manager(t, false)
	ctx := context.Background()
	if err := m.Revert(ctx); !errors.Is(err, ErrNoPending) {
		t.Fatalf("got %v", err)
	}
	if _, err := m.Apply(ctx, input(80)); err != nil {
		t.Fatal(err)
	}
	if err := m.Revert(ctx); err != nil {
		t.Fatal(err)
	}
	if h.has("iptables", "80") {
		t.Fatal("revert left rules")
	}
}

func TestRejectedRulesetTouchesNothing(t *testing.T) {
	h := newHarness(t)
	h.fake.rejectV6 = true
	m := h.manager(t, true)
	if _, err := m.Apply(context.Background(), input(80)); err == nil {
		t.Fatal("expected rejection")
	}
	if h.has("iptables", "80") || h.fake.hook["iptables"] {
		t.Fatal("v4 was applied although v6 failed --test")
	}
	if _, err := m.Pending(context.Background()); !errors.Is(err, ErrNoPending) {
		t.Fatal("pending row written for a rejected ruleset")
	}
}

func TestPartialApplyRollsBackFirstFamily(t *testing.T) {
	h := newHarness(t)
	h.fake.failApply["ip6tables-restore"] = true
	m := h.manager(t, true)
	if _, err := m.Apply(context.Background(), input(80)); err == nil {
		t.Fatal("expected failure")
	}
	if h.has("iptables", "80") || h.fake.hook["iptables"] {
		t.Fatal("v4 left applied after v6 failed")
	}
	if _, err := m.Pending(context.Background()); !errors.Is(err, ErrNoPending) {
		t.Fatal("pending row left behind")
	}
}

func TestSnapshotKeepsOnlyOwnedChains(t *testing.T) {
	h := newHarness(t)
	m := h.manager(t, false)
	s, err := m.snapshot(context.Background(), famCmds{fam4, "iptables", "iptables-save", "iptables-restore"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.Lines, "DOCKER") {
		t.Fatalf("snapshot captured a foreign chain: %q", s.Lines)
	}
}
