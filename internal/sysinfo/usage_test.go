package sysinfo

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/docker"
)

type mapFiles map[string]string

func (m mapFiles) ReadFile(p string) ([]byte, error) {
	if s, ok := m[p]; ok {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("no such file %s", p)
}

type batchCmd struct {
	out   string
	calls int
	args  []string
}

func (b *batchCmd) Output(_ context.Context, _ string, args ...string) ([]byte, error) {
	b.calls++
	b.args = args
	return []byte(b.out), nil
}

const showBlocks = `Id=panel.service
LoadState=loaded
ActiveState=active
SubState=running
ControlGroup=/system.slice/panel.service

Id=panel-nginx.service
LoadState=loaded
ActiveState=active
SubState=running
ControlGroup=/system.slice/panel-nginx.service

Id=panel-supervisord.service
LoadState=loaded
ActiveState=inactive
SubState=dead
ControlGroup=

Id=panel-firewall.service
LoadState=not-found
ActiveState=inactive
SubState=dead

Id=docker.service
LoadState=loaded
ActiveState=failed
SubState=failed
ControlGroup=/system.slice/docker.service
`

func usageServices(t *testing.T, files mapFiles, clk *clock, eng Engine, apps Apps) (*Services, *batchCmd) {
	t.Helper()
	cmd := &batchCmd{out: showBlocks}
	o := ServicesOptions{Cmd: cmd, Files: files, Now: clk.now, Apps: apps,
		Containers: func(context.Context) ([]string, error) { return []string{"db"}, nil }}
	if eng != nil {
		o.Engine = eng
	}
	s, err := NewServices(o)
	if err != nil {
		t.Fatal(err)
	}
	return s, cmd
}

func byUsageName(list []ServiceUsage) map[string]ServiceUsage {
	m := map[string]ServiceUsage{}
	for _, u := range list {
		m[u.Name] = u
	}
	return m
}

func TestUsageOneExecAndCgroupCPU(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	files := mapFiles{
		"/sys/fs/cgroup/system.slice/panel-nginx.service/cpu.stat":       "usage_usec 1000000\nuser_usec 800000\n",
		"/sys/fs/cgroup/system.slice/panel-nginx.service/memory.current": "52428800\n",
		"/sys/fs/cgroup/system.slice/panel.service/cpu.stat":             "usage_usec 0\n",
		"/sys/fs/cgroup/system.slice/panel.service/memory.current":       "1024\n",
	}
	s, cmd := usageServices(t, files, clk, nil, nil)

	first := byUsageName(s.Usage(context.Background()))
	if cmd.calls != 1 {
		t.Fatalf("systemctl ran %d times, want one batched call", cmd.calls)
	}
	if !strings.Contains(strings.Join(cmd.args, " "), "panel-nginx.service") || !strings.Contains(strings.Join(cmd.args, " "), "docker.service") {
		t.Errorf("the batch did not name every unit: %v", cmd.args)
	}
	ng := first["panel-nginx"]
	if ng.State != StateRunning || ng.CPUOK || !ng.RSSOK || ng.RSSBytes != 52428800 {
		t.Errorf("nginx first reading = %+v (memory yes, cpu needs two readings)", ng)
	}

	// 10 s later the cgroup has burned 2 more CPU seconds: 20% of one core.
	clk.advance(10 * time.Second)
	files["/sys/fs/cgroup/system.slice/panel-nginx.service/cpu.stat"] = "usage_usec 3000000\n"
	second := byUsageName(s.Usage(context.Background()))
	ng = second["panel-nginx"]
	if !ng.CPUOK || math.Abs(ng.CPUPercent-20) > 1e-9 {
		t.Errorf("nginx cpu = %v ok=%v, want 20", ng.CPUPercent, ng.CPUOK)
	}

	// A stopped, a missing and a failed unit keep their state and have no figures.
	for name, want := range map[string]State{"panel-supervisord": StateStopped, "panel-firewall": StateMissing, "docker": StateFailed} {
		u := second[name]
		if u.State != want || u.CPUOK || u.RSSOK {
			t.Errorf("%s = %+v, want state %s and no figures", name, u, want)
		}
	}
}

func TestUsageCounterGoingBackwardsGivesNoFigure(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	const p = "/sys/fs/cgroup/system.slice/panel-nginx.service/"
	files := mapFiles{p + "cpu.stat": "usage_usec 9000000\n"}
	s, _ := usageServices(t, files, clk, nil, nil)
	s.Usage(context.Background())
	clk.advance(5 * time.Second)
	files[p+"cpu.stat"] = "usage_usec 100\n" // service restarted: the cgroup is new
	if u := byUsageName(s.Usage(context.Background()))["panel-nginx"]; u.CPUOK {
		t.Errorf("cpu figure after a counter reset: %+v", u)
	}
}

func TestUsageContainerThroughProcCgroup(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	files := mapFiles{
		"/proc/4242/cgroup": "0::/system.slice/docker-abc123.scope\n",
		"/sys/fs/cgroup/system.slice/docker-abc123.scope/cpu.stat":       "usage_usec 500000\n",
		"/sys/fs/cgroup/system.slice/docker-abc123.scope/memory.current": "8388608\n",
	}
	eng := &fakeEngine{containers: map[string]docker.Container{
		"db": {State: docker.ContainerState{Status: "running", Running: true, Pid: 4242}},
	}}
	s, _ := usageServices(t, files, clk, eng, nil)
	u := byUsageName(s.Usage(context.Background()))["db"]
	if u.Kind != KindContainer || u.State != StateRunning || !u.RSSOK || u.RSSBytes != 8388608 {
		t.Errorf("container = %+v", u)
	}
}

func TestUsageAppByPID(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	stat := func(ticks int) string {
		// pid (comm with space) S ppid pgrp session tty tpgid flags minflt cminflt majflt cmajflt utime stime ...
		return fmt.Sprintf("777 (my app) S 1 777 777 0 -1 4194560 100 0 0 0 %d %d 0 0 20 0 1 0 1 1000 1000", ticks, ticks)
	}
	files := mapFiles{
		"/proc/777/stat":   stat(100),
		"/proc/777/status": "Name:\tmy app\nVmRSS:\t   2048 kB\n",
	}
	apps := &fakeApps{list: []AppState{{Name: "blog", State: "RUNNING", PID: 777}, {Name: "idle", State: "STOPPED"}}}
	s, _ := usageServices(t, files, clk, nil, apps)
	s.Usage(context.Background())
	clk.advance(10 * time.Second)
	files["/proc/777/stat"] = stat(150) // +50 utime +50 stime ticks = 1 s of CPU over 10 s
	got := byUsageName(s.Usage(context.Background()))
	blog := got[AppPrefix+"blog"]
	if blog.Kind != KindApp || !blog.CPUOK || math.Abs(blog.CPUPercent-10) > 1e-9 || blog.RSSBytes != 2048*1024 {
		t.Errorf("app = %+v, want 10%% cpu and 2 MiB", blog)
	}
	if idle := got[AppPrefix+"idle"]; idle.State != StateStopped || idle.CPUOK || idle.RSSOK {
		t.Errorf("stopped app = %+v", idle)
	}

	// A new PID under the same name is a new process: no figure until two readings.
	apps.list[0].PID = 888
	files["/proc/888/stat"] = stat(5)
	files["/proc/888/status"] = "VmRSS: 100 kB\n"
	clk.advance(10 * time.Second)
	if u := byUsageName(s.Usage(context.Background()))[AppPrefix+"blog"]; u.CPUOK {
		t.Errorf("cpu figure carried across a pid change: %+v", u)
	}
}

func TestParsersTolerateOddInput(t *testing.T) {
	if _, ok := ParseCgroupCPUUsec([]byte("user_usec 5\n")); ok {
		t.Error("usage_usec found where there is none")
	}
	if _, ok := ParseCgroupMemory([]byte("max\n")); ok {
		t.Error(`"max" parsed as a number`)
	}
	if p, ok := ParseProcCgroup([]byte("12:cpu:/x\n0::/system.slice/a.service\n")); !ok || p != "/system.slice/a.service" {
		t.Errorf("ParseProcCgroup = %q %v", p, ok)
	}
	if _, ok := ParseProcCgroup([]byte("12:cpu:/x\n")); ok {
		t.Error("a cgroup v1 file gave a v2 path")
	}
	if _, err := ParseProcStatUsec([]byte("1 (x) S 1")); err == nil {
		t.Error("truncated stat accepted")
	}
	if _, err := ParseProcStatUsec([]byte("garbage")); err == nil {
		t.Error("stat without a command accepted")
	}
	if _, ok := ParseProcVmRSS([]byte("Name: x\n")); ok {
		t.Error("VmRSS found where there is none (kernel thread)")
	}
	blocks := ParseSystemdShowBlocks([]byte("Id=a.service\nActiveState=active\n\n\nId=b.service\nActiveState=failed\n"))
	if len(blocks) != 2 || blocks["b.service"]["ActiveState"] != "failed" {
		t.Errorf("blocks = %v", blocks)
	}
}
