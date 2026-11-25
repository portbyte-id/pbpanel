package sysinfo

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ServiceUsage is one service's state and, where the host can say, its CPU and
// memory. A stopped unit, a host without cgroup v2 or an app without a PID has a
// state and nothing else: CPUOK and RSSOK say which figures exist.
type ServiceUsage struct {
	Name  string
	Kind  Kind
	State State
	// CPUPercent is percent of one core since the previous Usage call; it needs
	// two readings of the same process or cgroup, so it is absent on the first.
	CPUPercent float64
	CPUOK      bool
	RSSBytes   uint64
	RSSOK      bool
}

type cpuReading struct {
	usec uint64
	at   time.Time
	// id identifies the thing measured (cgroup path or "pid:N"): a restarted
	// process has a new one, and the counters of the old one mean nothing.
	id string
}

type usageState struct {
	mu   sync.Mutex
	prev map[string]cpuReading
}

// Usage reads every service's state and resource figures. All systemd units are
// asked in one `systemctl show` rather than one exec each. It never fails as a
// whole: a service that cannot be read keeps its state and loses its figures.
func (s *Services) Usage(ctx context.Context) []ServiceUsage {
	s.usage.mu.Lock()
	defer s.usage.mu.Unlock()
	if s.usage.prev == nil {
		s.usage.prev = map[string]cpuReading{}
	}
	now := s.now()
	seen := map[string]bool{}
	var out []ServiceUsage

	units := s.allUnits(ctx)
	shown := s.showUnits(ctx, units)
	for _, u := range units {
		blk, ok := shown[u.unit]
		var st State
		cg := ""
		switch {
		case !ok:
			st = StateUnknown
		default:
			st, _ = parseSystemdShow(blockBytes(blk))
			cg = blk["ControlGroup"]
		}
		su := ServiceUsage{Name: u.name, Kind: KindSystemd, State: st}
		if st == StateRunning || st == StateStarting || st == StateStopping {
			s.cgroupUsage(&su, "cg:"+u.name, cg, now, seen)
		}
		out = append(out, su)
	}

	if names, err := s.containerNames(ctx); err == nil && s.eng != nil {
		for _, name := range names {
			st, _ := s.containerState(ctx, name)
			su := ServiceUsage{Name: name, Kind: KindContainer, State: st}
			if st == StateRunning {
				if c, err := s.eng.ContainerInspect(ctx, name); err == nil && c.State.Pid > 0 {
					if b, err := s.files.ReadFile("/proc/" + strconv.Itoa(c.State.Pid) + "/cgroup"); err == nil {
						if cg, ok := ParseProcCgroup(b); ok {
							s.cgroupUsage(&su, "ct:"+name, cg, now, seen)
						}
					}
				}
			}
			out = append(out, su)
		}
	}

	if s.apps != nil {
		if list, err := s.apps.List(ctx); err == nil {
			for _, a := range list {
				su := ServiceUsage{Name: AppPrefix + a.Name, Kind: KindApp, State: appState(a.State)}
				if su.State == StateRunning && a.PID > 0 {
					s.pidUsage(&su, "app:"+a.Name, a.PID, now, seen)
				}
				out = append(out, su)
			}
		}
	}

	// Forget services that are gone, so a name reused later does not inherit a
	// stale reading.
	for k := range s.usage.prev {
		if !seen[k] {
			delete(s.usage.prev, k)
		}
	}
	return out
}

// showUnits is one `systemctl show` for every unit. A failed exec returns an
// empty map, which makes every unit state unknown for this round.
func (s *Services) showUnits(ctx context.Context, units []unit) map[string]map[string]string {
	args := []string{"show", "--no-pager", "--property=Id,LoadState,ActiveState,SubState,ControlGroup"}
	for _, u := range units {
		args = append(args, u.unit)
	}
	out, err := s.cmd.Output(ctx, "systemctl", args...)
	if err != nil && len(out) == 0 {
		return map[string]map[string]string{}
	}
	return ParseSystemdShowBlocks(out)
}

func blockBytes(blk map[string]string) []byte {
	var b strings.Builder
	for k, v := range blk {
		b.WriteString(k + "=" + v + "\n")
	}
	return []byte(b.String())
}

func (s *Services) cgroupUsage(su *ServiceUsage, key, cg string, now time.Time, seen map[string]bool) {
	if cg == "" || !strings.HasPrefix(cg, "/") {
		return
	}
	dir := cgroupRoot + cg
	if b, err := s.files.ReadFile(dir + "/memory.current"); err == nil {
		su.RSSBytes, su.RSSOK = ParseCgroupMemory(b)
	}
	b, err := s.files.ReadFile(dir + "/cpu.stat")
	if err != nil {
		return
	}
	if usec, ok := ParseCgroupCPUUsec(b); ok {
		s.cpuDelta(su, key, cg, usec, now, seen)
	}
}

func (s *Services) pidUsage(su *ServiceUsage, key string, pid int, now time.Time, seen map[string]bool) {
	p := "/proc/" + strconv.Itoa(pid)
	if b, err := s.files.ReadFile(p + "/status"); err == nil {
		su.RSSBytes, su.RSSOK = ParseProcVmRSS(b)
	}
	b, err := s.files.ReadFile(p + "/stat")
	if err != nil {
		return
	}
	if usec, err := ParseProcStatUsec(b); err == nil {
		s.cpuDelta(su, key, "pid:"+strconv.Itoa(pid), usec, now, seen)
	}
}

// cpuDelta turns a cumulative CPU counter into percent of one core since the
// last reading of the same thing. A changed id, a counter that went backwards or
// a zero interval gives no figure rather than a wrong one.
func (s *Services) cpuDelta(su *ServiceUsage, key, id string, usec uint64, now time.Time, seen map[string]bool) {
	seen[key] = true
	prev, ok := s.usage.prev[key]
	s.usage.prev[key] = cpuReading{usec: usec, at: now, id: id}
	if !ok || prev.id != id || usec < prev.usec {
		return
	}
	dt := now.Sub(prev.at).Microseconds()
	if dt <= 0 {
		return
	}
	su.CPUPercent = float64(usec-prev.usec) / float64(dt) * 100
	su.CPUOK = true
}
