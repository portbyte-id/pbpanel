package firewall

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestRuleValidate(t *testing.T) {
	ok := Rule{PortStart: 80, PortEnd: 80, Protocol: ProtoTCP, Enabled: true}
	tests := []struct {
		name string
		mod  func(*Rule)
		want error
	}{
		{"valid", func(*Rule) {}, nil},
		{"range", func(r *Rule) { r.PortStart, r.PortEnd = 1000, 2000 }, nil},
		{"port zero", func(r *Rule) { r.PortStart = 0 }, ErrPort},
		{"port high", func(r *Rule) { r.PortEnd = 65536 }, ErrPort},
		{"inverted range", func(r *Rule) { r.PortStart, r.PortEnd = 90, 80 }, ErrPortOrd},
		{"bad proto", func(r *Rule) { r.Protocol = "icmp" }, ErrProto},
		{"both", func(r *Rule) { r.Protocol = ProtoBoth }, nil},
		{"v4 cidr", func(r *Rule) { r.Source = "10.0.0.0/8" }, nil},
		{"v6 cidr", func(r *Rule) { r.Source = "2001:db8::/32" }, nil},
		{"bare address", func(r *Rule) { r.Source = "192.0.2.1" }, nil},
		{"garbage source", func(r *Rule) { r.Source = "10.0.0.0/33" }, ErrAddr},
		{"mapped v6", func(r *Rule) { r.Source = "::ffff:10.0.0.1" }, ErrAddr},
		{"zoned", func(r *Rule) { r.Source = "fe80::1%eth0" }, ErrAddr},
		{"quote in comment", func(r *Rule) { r.Comment = `a"b` }, ErrComment},
		{"newline in comment", func(r *Rule) { r.Comment = "a\nb" }, ErrComment},
		{"long comment", func(r *Rule) { r.Comment = strings.Repeat("x", MaxCommentLen+1) }, ErrComment},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := ok
			tc.mod(&r)
			if err := r.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestOverlapsAllowed(t *testing.T) {
	a := Rule{PortStart: 80, PortEnd: 90, Protocol: ProtoTCP}
	b := Rule{PortStart: 85, PortEnd: 85, Protocol: ProtoTCP}
	if a.Validate() != nil || b.Validate() != nil {
		t.Fatal("overlapping rules must each validate")
	}
}

func TestBlockEntryValidate(t *testing.T) {
	if err := (BlockEntry{}).Validate(); !errors.Is(err, ErrEmptyNet) {
		t.Fatalf("empty: %v", err)
	}
	if err := (BlockEntry{Addr: "203.0.113.0/24"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (BlockEntry{Addr: "nope"}).Validate(); !errors.Is(err, ErrAddr) {
		t.Fatalf("bad: %v", err)
	}
}

func TestParsePrefixMasks(t *testing.T) {
	p, err := ParsePrefix("10.1.2.3/8")
	if err != nil || p.String() != "10.0.0.0/8" {
		t.Fatalf("got %v %v", p, err)
	}
}

type fakeRun struct {
	out []byte
	err error
}

func (f fakeRun) Output(context.Context, string, ...string) ([]byte, error) { return f.out, f.err }

func TestDetectSSHPorts(t *testing.T) {
	sshdT := []byte("port 2222\nport 22\nlistenaddress 0.0.0.0\nport 2222\n")
	cfg := fstest.MapFS{
		"etc/ssh/sshd_config":           {Data: []byte("# c\nPort 22\nInclude /etc/ssh/sshd_config.d/*.conf\nMatch User x\nPort 9999\n")},
		"etc/ssh/sshd_config.d/10.conf": {Data: []byte("Port 2200\n")},
		"etc/ssh/sshd_config.d/20.conf": {Data: []byte("port 2201\nPort bad\n")},
	}
	rel := fstest.MapFS{
		"etc/ssh/sshd_config":   {Data: []byte("Include conf.d/*.conf\n")},
		"etc/ssh/conf.d/a.conf": {Data: []byte("Port 2300\n")},
	}
	loop := fstest.MapFS{"etc/ssh/sshd_config": {Data: []byte("Include /etc/ssh/sshd_config\nPort 22\n")}}

	tests := []struct {
		name string
		run  Outputter
		fs   fstest.MapFS
		want []int
	}{
		{"sshd -T wins and dedupes", fakeRun{out: sshdT}, cfg, []int{22, 2222}},
		{"sshd missing falls back to config and includes", fakeRun{err: errors.New("nf")}, cfg, []int{22, 2200, 2201}},
		{"nil runner", nil, cfg, []int{22, 2200, 2201}},
		{"relative include", nil, rel, []int{2300}},
		{"include loop terminates", nil, loop, []int{22}},
		{"nothing readable defaults to 22", nil, fstest.MapFS{}, []int{22}},
		{"sshd -T with no port lines falls through", fakeRun{out: []byte("x y\n")}, rel, []int{2300}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectSSHPorts(context.Background(), tc.run, tc.fs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func sampleInput() RenderInput {
	return RenderInput{
		SSHPorts:  []int{22, 2222},
		PanelPort: 8443,
		HookV4:    true, HookV6: true,
		Rules: []Rule{
			{ID: 1, PortStart: 80, PortEnd: 80, Protocol: ProtoTCP, Enabled: true, Comment: "http"},
			{ID: 2, PortStart: 443, PortEnd: 443, Protocol: ProtoBoth, Enabled: true},
			{ID: 3, PortStart: 5000, PortEnd: 5100, Protocol: ProtoUDP, Enabled: true, Source: "10.0.0.0/8"},
			{ID: 4, PortStart: 3306, PortEnd: 3306, Protocol: ProtoTCP, Enabled: true, Source: "2001:db8::/32"},
			{ID: 5, PortStart: 25, PortEnd: 25, Protocol: ProtoTCP, Enabled: false},
			{ID: 6, PortStart: 22, PortEnd: 22, Protocol: ProtoTCP, Enabled: true, Source: "192.0.2.7"},
		},
		Blocklist: []BlockEntry{
			{ID: 1, Addr: "203.0.113.0/24", Comment: "scanner"},
			{ID: 2, Addr: "2001:db8:bad::/48"},
		},
	}
}

func TestRenderGolden(t *testing.T) {
	r := Render(sampleInput())
	for name, got := range map[string]string{"v4.golden": r.V4, "v6.golden": r.V6} {
		path := filepath.Join("testdata", name)
		if *update {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (run with -update)", err)
		}
		if string(want) != got {
			t.Errorf("%s differs:\n%s", name, got)
		}
	}
}

func TestRenderTouchesOnlyOwnedChains(t *testing.T) {
	r := Render(sampleInput())
	owned := map[string]bool{ChainInput: true, ChainBlock: true}
	targets := map[string]bool{"ACCEPT": true, "DROP": true, ChainInput: true, ChainBlock: true}
	for fam, text := range map[string]string{"v4": r.V4, "v6": r.V6} {
		for l := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
			f := strings.Fields(l)
			switch {
			case l == "*filter" || l == "COMMIT":
			case strings.HasPrefix(l, ":"):
				if !owned[f[0][1:]] {
					t.Errorf("%s: declares foreign chain: %s", fam, l)
				}
			case f[0] == "-F" || f[0] == "-A":
				if !owned[f[1]] {
					t.Errorf("%s: touches foreign chain: %s", fam, l)
				}
			case f[0] == "-I":
				if l != "-I INPUT 1 -j "+ChainInput {
					t.Errorf("%s: unexpected insert: %s", fam, l)
				}
			default:
				t.Errorf("%s: unexpected line: %s", fam, l)
			}
			if i := indexOf(f, "-j"); i >= 0 && !targets[f[i+1]] {
				t.Errorf("%s: jumps to foreign target: %s", fam, l)
			}
			if strings.Contains(l, "DOCKER") || strings.HasPrefix(l, "-P ") {
				t.Errorf("%s: touches Docker or policy: %s", fam, l)
			}
		}
	}
}

func TestRenderFamilySplit(t *testing.T) {
	r := Render(sampleInput())
	if strings.Contains(r.V4, "2001:db8") || strings.Contains(r.V6, "10.0.0.0") || strings.Contains(r.V6, "192.0.2.7") {
		t.Fatal("rule leaked into the wrong family")
	}
	if !strings.Contains(r.V4, "--dport 80") || !strings.Contains(r.V6, "--dport 80") {
		t.Fatal("source-less rule must appear in both families")
	}
	if strings.Contains(r.V4, "--dport 25 ") {
		t.Fatal("disabled rule rendered")
	}
	if !strings.Contains(r.V6, "--icmpv6-type 135") || !strings.Contains(r.V6, "--icmpv6-type 136") {
		t.Fatal("neighbour discovery missing from v6")
	}
}

func TestRenderBuiltinsAlwaysPresent(t *testing.T) {
	r := Render(RenderInput{SSHPorts: []int{22}, PanelPort: 22}) // duplicate port
	if n := strings.Count(r.V4, "--dport 22 "); n != 1 {
		t.Fatalf("built-in port 22 rendered %d times", n)
	}
	if strings.Contains(r.V4, "-I INPUT") {
		t.Fatal("jump emitted without a hook flag")
	}
	if !strings.Contains(r.V4, "-A PANEL-INPUT -j DROP") {
		t.Fatal("final DROP missing")
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
