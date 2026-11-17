package sysinfo

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestParseCPUStat(t *testing.T) {
	total, cores, err := ParseCPUStat(fixture(t, "stat"))
	if err != nil {
		t.Fatal(err)
	}
	want := CPUTimes{User: 4705, Nice: 150, System: 3120, Idle: 80000, IOWait: 200, SoftIRQ: 45, Steal: 10}
	if total != want {
		t.Fatalf("total = %+v, want %+v", total, want)
	}
	if len(cores) != 2 || cores[1].User != 2305 {
		t.Fatalf("cores = %+v", cores)
	}
	if got := total.Total(); got != 88230 {
		t.Fatalf("Total() = %d", got)
	}
}

func TestParseCPUStatOldKernel(t *testing.T) {
	total, _, err := ParseCPUStat([]byte("cpu 1 2 3 4\n"))
	if err != nil || total.Idle != 4 || total.IOWait != 0 {
		t.Fatalf("got %+v, %v", total, err)
	}
}

func TestParseCPUStatErrors(t *testing.T) {
	for name, in := range map[string]string{
		"empty":      "",
		"no cpu":     "intr 1 2 3\n",
		"short line": "cpu 1 2 3\n",
		"not number": "cpu 1 2 x 4\n",
	} {
		if _, _, err := ParseCPUStat([]byte(in)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestCPUPercent(t *testing.T) {
	prev := CPUTimes{User: 100, Idle: 900}
	cur := CPUTimes{User: 150, Idle: 950} // 100 ticks, 50 idle
	if got := CPUPercent(prev, cur); !near(got, 50) {
		t.Fatalf("got %v", got)
	}
	if got := CPUPercent(cur, prev); got != 0 {
		t.Fatalf("backwards counters: %v", got)
	}
	if got := CPUPercent(cur, cur); got != 0 {
		t.Fatalf("no ticks: %v", got)
	}
	// I/O wait is idle time.
	if got := CPUPercent(CPUTimes{Idle: 10}, CPUTimes{Idle: 10, IOWait: 90}); got != 0 {
		t.Fatalf("iowait counted busy: %v", got)
	}
}

func TestParseLoadavg(t *testing.T) {
	l, err := ParseLoadavg(fixture(t, "loadavg"))
	if err != nil {
		t.Fatal(err)
	}
	if l != (Load{One: 0.52, Five: 0.58, Fifteen: 0.59, Running: 3, Total: 467}) {
		t.Fatalf("got %+v", l)
	}
	for _, in := range []string{"", "0.1 0.2 0.3", "a b c 1/2 3", "0.1 0.2 0.3 12 3"} {
		if _, err := ParseLoadavg([]byte(in)); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}

func TestParseMeminfo(t *testing.T) {
	m, err := ParseMeminfo(fixture(t, "meminfo"))
	if err != nil {
		t.Fatal(err)
	}
	want := Memory{
		TotalBytes: 8000000 * 1024, AvailableBytes: 4000000 * 1024, UsedBytes: 4000000 * 1024,
		FreeBytes: 1000000 * 1024, BuffersBytes: 200000 * 1024, CachedBytes: 2500000 * 1024,
		SwapTotalBytes: 2000000 * 1024, SwapFreeBytes: 1500000 * 1024, SwapUsedBytes: 500000 * 1024,
	}
	if m != want {
		t.Fatalf("got %+v\nwant %+v", m, want)
	}
}

func TestParseMeminfoWithoutMemAvailable(t *testing.T) {
	m, err := ParseMeminfo(fixture(t, "meminfo_old"))
	if err != nil {
		t.Fatal(err)
	}
	if m.AvailableBytes != 300*1024 || m.UsedBytes != 700*1024 || m.SwapUsedBytes != 0 {
		t.Fatalf("got %+v", m)
	}
	if _, err := ParseMeminfo([]byte("MemFree: 1 kB\n")); err == nil {
		t.Fatal("meminfo without MemTotal accepted")
	}
}

func TestParseUptime(t *testing.T) {
	d, err := ParseUptime(fixture(t, "uptime"))
	if err != nil {
		t.Fatal(err)
	}
	if d != 350735470*time.Millisecond {
		t.Fatalf("got %v", d)
	}
	for _, in := range []string{"", "x 1", "-5 1"} {
		if _, err := ParseUptime([]byte(in)); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}

func TestParseNetDev(t *testing.T) {
	got, err := ParseNetDev(fixture(t, "net_dev"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d interfaces: %+v", len(got), got)
	}
	want := NetCounters{Iface: "eth0", RxBytes: 1000000, RxPackets: 900, RxErrors: 2, TxBytes: 500000, TxPackets: 700, TxErrors: 1}
	if got[1] != want {
		t.Fatalf("eth0 = %+v, want %+v", got[1], want)
	}
	// "docker0:12345" has no space after the colon.
	if got[2].Iface != "docker0" || got[2].RxBytes != 12345 || got[2].TxBytes != 6789 {
		t.Fatalf("docker0 = %+v", got[2])
	}
	if _, err := ParseNetDev([]byte("eth0: 1 2 3\n")); err == nil {
		t.Fatal("short interface line accepted")
	}
}

func TestParseMountsAndDiskMounts(t *testing.T) {
	all, err := ParseMounts(fixture(t, "mounts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 10 {
		t.Fatalf("parsed %d mounts", len(all))
	}
	got := DiskMounts(all)
	var points []string
	for _, m := range got {
		points = append(points, m.Point)
	}
	want := []string{"/", "/mnt/data disk", "/boot/efi"}
	if !reflect.DeepEqual(points, want) {
		t.Fatalf("disk mounts = %q, want %q", points, want)
	}
}

func TestUnescapeMount(t *testing.T) {
	for in, want := range map[string]string{
		`/plain`:          "/plain",
		`/a\040b`:         "/a b",
		`/t\011\012x`:     "/t\t\nx",
		`/back\134slash`:  `/back\slash`,
		`/trunc\04`:       `/trunc\04`,
		`/notoctal\zzzq`:  `/notoctal\zzzq`,
		`trailing\`:       `trailing\`,
		`/two\040\040end`: "/two  end",
	} {
		if got := unescapeMount(in); got != want {
			t.Errorf("unescapeMount(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := ParseMounts([]byte("only two\n")); err == nil || !strings.Contains(err.Error(), "short line") {
		t.Fatalf("short mounts line: %v", err)
	}
}
