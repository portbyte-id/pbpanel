package firewall

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

type fakeMasker struct {
	calls int
	err   error
}

func (f *fakeMasker) MaskFirewallServices(context.Context) error { f.calls++; return f.err }

func newService(t *testing.T, h *harness, ipv6 bool, mk *fakeMasker) *Service {
	t.Helper()
	s, err := NewService(ServiceOptions{
		Manager: h.manager(t, ipv6), Store: h.st, Cmd: h.fake, Masker: mk,
		PanelPort: func() int { return 8443 },
		Root:      fstest.MapFS{"etc/ssh/sshd_config": {Data: []byte("Port 2222\n")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRuleCRUD(t *testing.T) {
	h := newHarness(t)
	s := newService(t, h, false, &fakeMasker{})
	ctx := context.Background()

	r, err := s.AddRule(ctx, Rule{PortStart: 80, PortEnd: 80, Protocol: ProtoTCP, Source: "10.1.2.3", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID == 0 || r.Source != "10.1.2.3/32" {
		t.Fatalf("not normalised: %+v", r)
	}
	if _, err := s.AddRule(ctx, Rule{PortStart: 0, PortEnd: 0, Protocol: ProtoTCP}); !errors.Is(err, ErrPort) {
		t.Fatalf("invalid rule stored: %v", err)
	}
	r2, err := s.UpdateRule(ctx, r.ID, Rule{PortStart: 81, PortEnd: 90, Protocol: ProtoBoth, Comment: "x"})
	if err != nil || r2.PortEnd != 90 {
		t.Fatalf("update: %+v %v", r2, err)
	}
	if _, err := s.UpdateRule(ctx, 999, Rule{PortStart: 1, PortEnd: 1, Protocol: ProtoTCP}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	got, _ := s.Rules(ctx)
	if len(got) != 1 || got[0].Enabled || got[0].Comment != "x" {
		t.Fatalf("stored rules: %+v", got)
	}
	if err := s.DeleteRule(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRule(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestBlocklistCRUD(t *testing.T) {
	h := newHarness(t)
	s := newService(t, h, false, &fakeMasker{})
	ctx := context.Background()
	e, err := s.AddBlock(ctx, BlockEntry{Addr: "203.0.113.9/24"})
	if err != nil || e.Addr != "203.0.113.0/24" {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := s.AddBlock(ctx, BlockEntry{Addr: "203.0.113.0/24"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.AddBlock(ctx, BlockEntry{Addr: "bad"}); !errors.Is(err, ErrAddr) {
		t.Fatalf("got %v", err)
	}
	if err := s.DeleteBlock(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
}

func TestEnableMasksThenAppliesAndConfirmEnables(t *testing.T) {
	h := newHarness(t)
	mk := &fakeMasker{}
	s := newService(t, h, false, mk)
	ctx := context.Background()

	if _, err := s.Apply(ctx); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("apply before enable: %v", err)
	}
	p, err := s.Enable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mk.calls != 1 {
		t.Fatal("the distribution firewall service was not masked before the first apply")
	}
	if !h.has("iptables", "2222") || !h.has("iptables", "8443") {
		t.Fatal("detected SSH port and panel port must be live")
	}
	st, _ := s.State(ctx)
	if st.Enabled || st.Pending == nil {
		t.Fatalf("enabled before confirm: %+v", st)
	}
	if err := s.Confirm(ctx, p.Token); err != nil {
		t.Fatal(err)
	}
	st, _ = s.State(ctx)
	if !st.Enabled || st.Dirty || st.Pending != nil || !reflect.DeepEqual(st.SSHPorts, []int{2222}) {
		t.Fatalf("after confirm: %+v", st)
	}
}

func TestEnableMaskFailureAppliesNothing(t *testing.T) {
	h := newHarness(t)
	s := newService(t, h, false, &fakeMasker{err: errors.New("boom")})
	if _, err := s.Enable(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if h.has("iptables", "8443") || h.fake.hook["iptables"] {
		t.Fatal("rules applied despite a failed mask")
	}
}

func TestDirtyAfterRuleChange(t *testing.T) {
	h := newHarness(t)
	s := newService(t, h, true, &fakeMasker{})
	ctx := context.Background()
	p, _ := s.Enable(ctx)
	_ = s.Confirm(ctx, p.Token)
	if _, err := s.AddRule(ctx, Rule{PortStart: 8080, PortEnd: 8080, Protocol: ProtoTCP, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.State(ctx)
	if !st.Dirty {
		t.Fatal("stored rule not live yet; must be dirty")
	}
	p, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Confirm(ctx, p.Token)
	if st, _ = s.State(ctx); st.Dirty {
		t.Fatal("dirty after applying and confirming")
	}
}

func TestDisableFlushesAndForgets(t *testing.T) {
	h := newHarness(t)
	s := newService(t, h, true, &fakeMasker{})
	ctx := context.Background()
	p, _ := s.Enable(ctx)
	_ = s.Confirm(ctx, p.Token)
	if err := s.Disable(ctx); err != nil {
		t.Fatal(err)
	}
	if h.fake.hook["iptables"] || h.fake.hook["ip6tables"] || len(h.fake.chains["iptables:"+ChainInput]) != 0 {
		t.Fatal("chains still hooked or populated")
	}
	if st, _ := s.State(ctx); st.Enabled {
		t.Fatal("still enabled")
	}
	// A reboot restore must now do nothing.
	h.fake.restores = nil
	if err := RestoreConfirmed(ctx, h.st, h.fake); err != nil || len(h.fake.restores) != 0 {
		t.Fatalf("restore after disable: %v %v", err, h.fake.restores)
	}
}

func TestDisableCancelsPending(t *testing.T) {
	h := newHarness(t)
	s := newService(t, h, false, &fakeMasker{})
	ctx := context.Background()
	if _, err := s.Enable(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Disable(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.State(ctx); st.Pending != nil {
		t.Fatal("pending survived disable")
	}
	h.tm.fireAll() // the cancelled timer must not roll anything back or error
}

func TestRestoreConfirmedBootAndIdempotent(t *testing.T) {
	h := newHarness(t)
	s := newService(t, h, false, &fakeMasker{})
	ctx := context.Background()
	if _, err := s.AddRule(ctx, Rule{PortStart: 8080, PortEnd: 8080, Protocol: ProtoTCP, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Enable(ctx)
	_ = s.Confirm(ctx, p.Token)

	// Simulate a reboot: kernel state gone, database intact.
	h.fake.chains, h.fake.exists, h.fake.hook = map[string][]string{}, map[string]bool{}, map[string]bool{}
	h.fake.restores = nil
	if err := RestoreConfirmed(ctx, h.st, h.fake); err != nil {
		t.Fatal(err)
	}
	if !h.has("iptables", "8080") || !h.has("iptables", "8443") || !h.fake.hook["iptables"] {
		t.Fatal("boot restore did not rebuild the confirmed ruleset and its jump")
	}
	// Running it again must not insert a second jump.
	h.fake.restores = nil
	if err := RestoreConfirmed(ctx, h.st, h.fake); err != nil {
		t.Fatal(err)
	}
	if len(h.fake.restores) != 1 || strings.Contains(h.fake.restores[0], "-I INPUT") {
		t.Fatalf("second restore stacked the jump: %q", h.fake.restores)
	}
}

func TestRestoreNotEnabledIsNoop(t *testing.T) {
	h := newHarness(t)
	if err := RestoreConfirmed(context.Background(), h.st, h.fake); err != nil || len(h.fake.restores) != 0 {
		t.Fatalf("%v %v", err, h.fake.restores)
	}
}

func TestParsePublishedPorts(t *testing.T) {
	out := "web\t0.0.0.0:8080->80/tcp, :::8080->80/tcp, 9000/tcp\n" +
		"dns\t0.0.0.0:5353->53/udp\n" +
		"range\t0.0.0.0:7000-7002->7000-7002/tcp\n" +
		"none\t\n" +
		"junk line\n"
	got := ParsePublishedPorts(out)
	want := []PublishedPort{
		{"dns", 5353, "udp"}, {"range", 7000, "tcp"}, {"web", 8080, "tcp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestAllowlistCRUD(t *testing.T) {
	h := newHarness(t)
	s := newService(t, h, false, &fakeMasker{})
	ctx := context.Background()
	e, err := s.AddAllow(ctx, AllowEntry{Addr: "198.51.100.9", Comment: "office"})
	if err != nil || e.Addr != "198.51.100.9/32" {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := s.AddAllow(ctx, AllowEntry{Addr: "198.51.100.9/32"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.AddAllow(ctx, AllowEntry{Addr: "bad"}); !errors.Is(err, ErrAddr) {
		t.Fatalf("got %v", err)
	}
	list, err := s.Allowlist(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	in, err := s.input(ctx)
	if err != nil || len(in.Allowlist) != 1 {
		t.Fatalf("input: %+v %v", in.Allowlist, err)
	}
	if err := s.DeleteAllow(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAllow(ctx, e.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
