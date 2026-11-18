package firewall

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// portCmd records every iptables call and answers `-S INPUT` with a canned listing.
type portCmd struct {
	listing map[string]string // binary -> `iptables -S INPUT` output
	calls   []string
	fail    error
}

func (c *portCmd) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	c.calls = append(c.calls, name+" "+strings.Join(args, " "))
	return []byte(c.listing[name]), nil
}

func (c *portCmd) RunStdin(_ context.Context, _ string, name string, args ...string) error {
	c.calls = append(c.calls, name+" "+strings.Join(args, " "))
	return c.fail
}

func TestOpenPanelPortInsertsOneRulePerFamily(t *testing.T) {
	c := &portCmd{listing: map[string]string{}}
	if err := OpenPanelPort(context.Background(), c, 8443, true); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"iptables -S INPUT",
		"iptables -I INPUT 1 -p tcp -m tcp --dport 8443 -m comment --comment panel:open -j ACCEPT",
		"ip6tables -S INPUT",
		"ip6tables -I INPUT 1 -p tcp -m tcp --dport 8443 -m comment --comment panel:open -j ACCEPT",
	}
	assertCalls(t, c.calls, want)
}

func TestOpenPanelPortReplacesEarlierRule(t *testing.T) {
	c := &portCmd{listing: map[string]string{
		"iptables": "-P INPUT ACCEPT\n" +
			"-A INPUT -p tcp -m tcp --dport 9443 -m comment --comment panel:open -j ACCEPT\n" +
			"-A INPUT -p tcp -m tcp --dport 22 -j ACCEPT\n",
	}}
	if err := OpenPanelPort(context.Background(), c, 8443, false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"iptables -S INPUT",
		"iptables -D INPUT -p tcp -m tcp --dport 9443 -m comment --comment panel:open -j ACCEPT",
		"iptables -I INPUT 1 -p tcp -m tcp --dport 8443 -m comment --comment panel:open -j ACCEPT",
	}
	assertCalls(t, c.calls, want)
}

func TestOpenPanelPortLeavesOtherRulesAlone(t *testing.T) {
	c := &portCmd{listing: map[string]string{
		"iptables": "-A INPUT -p tcp -m tcp --dport 80 -j ACCEPT\n-A PBSERVER-INPUT -p tcp --dport 9443 -j ACCEPT\n",
	}}
	if err := OpenPanelPort(context.Background(), c, 8443, false); err != nil {
		t.Fatal(err)
	}
	for _, call := range c.calls {
		if strings.Contains(call, " -D ") {
			t.Fatalf("deleted a rule it does not own: %s", call)
		}
	}
}

func TestOpenPanelPortRejectsBadPort(t *testing.T) {
	c := &portCmd{listing: map[string]string{}}
	for _, p := range []int{0, -1, 65536} {
		if err := OpenPanelPort(context.Background(), c, p, false); err == nil {
			t.Fatalf("port %d accepted", p)
		}
	}
	if len(c.calls) != 0 {
		t.Fatalf("bad port ran commands: %v", c.calls)
	}
}

func TestOpenPanelPortReportsCommandFailure(t *testing.T) {
	boom := errors.New("iptables: no chain")
	c := &portCmd{listing: map[string]string{}, fail: boom}
	err := OpenPanelPort(context.Background(), c, 8443, false)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
}

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestOpenWebPortsOneRulePerSpecPerFamily(t *testing.T) {
	c := &portCmd{listing: map[string]string{}}
	if err := OpenWebPorts(context.Background(), c, true); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, bin := range []string{"iptables", "ip6tables"} {
		want = append(want, bin+" -S INPUT",
			bin+" -I INPUT 1 -p tcp -m tcp --dport 80 -m comment --comment panel:web -j ACCEPT",
			bin+" -I INPUT 1 -p tcp -m tcp --dport 443 -m comment --comment panel:web -j ACCEPT",
			bin+" -I INPUT 1 -p udp -m udp --dport 443 -m comment --comment panel:web -j ACCEPT")
	}
	assertCalls(t, c.calls, want)
}

func TestOpenWebPortsReplacesOldRulesAndKeepsOthers(t *testing.T) {
	c := &portCmd{listing: map[string]string{
		"iptables": "-A INPUT -p tcp -m tcp --dport 80 -m comment --comment panel:web -j ACCEPT\n" +
			"-A INPUT -p tcp -m tcp --dport 8443 -m comment --comment panel:open -j ACCEPT\n",
	}}
	if err := OpenWebPorts(context.Background(), c, false); err != nil {
		t.Fatal(err)
	}
	dels := 0
	for _, call := range c.calls {
		if strings.Contains(call, " -D ") {
			dels++
			if !strings.Contains(call, "panel:web") {
				t.Fatalf("deleted a foreign rule: %s", call)
			}
		}
	}
	if dels != 1 || len(c.calls) != 5 {
		t.Fatalf("dels=%d calls=%v", dels, c.calls)
	}
}
