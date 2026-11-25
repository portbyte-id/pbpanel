package sysinfo

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// FileReader reads a file by the absolute path the kernel presents it at. It is
// the seam under per-service usage: tests serve cgroup and /proc text from a map,
// the daemon reads the host.
type FileReader interface {
	ReadFile(path string) ([]byte, error)
}

// OSFiles reads the real filesystem.
type OSFiles struct{}

// ReadFile implements FileReader.
func (OSFiles) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

const cgroupRoot = "/sys/fs/cgroup"

// clockTicksPerSecond is USER_HZ, which is 100 on every Linux architecture the
// panel targets (sysconf(_SC_CLK_TCK) would need cgo).
const clockTicksPerSecond = 100

// ParseCgroupCPUUsec reads usage_usec from a cgroup v2 cpu.stat.
func ParseCgroupCPUUsec(data []byte) (uint64, bool) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), " ")
		if !ok || k != "usage_usec" {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		return n, err == nil
	}
	return 0, false
}

// ParseCgroupMemory reads a cgroup v2 memory.current.
func ParseCgroupMemory(data []byte) (uint64, bool) {
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return n, err == nil
}

// ParseProcCgroup returns the unified (cgroup v2) path from /proc/<pid>/cgroup,
// the "0::/system.slice/docker-<id>.scope" line. ok is false on a cgroup v1 host,
// where there is no such line.
func ParseProcCgroup(data []byte) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "0::"); ok && strings.HasPrefix(rest, "/") {
			return rest, true
		}
	}
	return "", false
}

// ParseProcStatUsec is a process's CPU time in microseconds: utime plus stime from
// /proc/<pid>/stat. The command name in field 2 may contain spaces and
// parentheses, so fields are counted from the last ')'.
func ParseProcStatUsec(data []byte) (uint64, error) {
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, fmt.Errorf("sysinfo: /proc/<pid>/stat has no command field")
	}
	f := strings.Fields(s[i+1:])
	// After the ')' the fields start at 3 (state), so utime (14) is f[11].
	if len(f) < 13 {
		return 0, fmt.Errorf("sysinfo: /proc/<pid>/stat is truncated")
	}
	ut, err := strconv.ParseUint(f[11], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("sysinfo: /proc/<pid>/stat utime: %w", err)
	}
	st, err := strconv.ParseUint(f[12], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("sysinfo: /proc/<pid>/stat stime: %w", err)
	}
	return (ut + st) * (1_000_000 / clockTicksPerSecond), nil
}

// ParseProcVmRSS reads VmRSS, in bytes, from /proc/<pid>/status.
func ParseProcVmRSS(data []byte) (uint64, bool) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		rest, ok := strings.CutPrefix(sc.Text(), "VmRSS:")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			return 0, false
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return 0, false
		}
		if len(f) > 1 && strings.EqualFold(f[1], "kB") {
			n *= 1024
		}
		return n, true
	}
	return 0, false
}

// ParseSystemdShowBlocks splits the output of one `systemctl show` over several
// units: one block of Key=Value lines per unit, separated by blank lines. The
// result is keyed by each block's Id.
func ParseSystemdShowBlocks(out []byte) map[string]map[string]string {
	res := map[string]map[string]string{}
	cur := map[string]string{}
	flush := func() {
		if id := cur["Id"]; id != "" {
			res[id] = cur
		}
		cur = map[string]string{}
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			cur[k] = v
		}
	}
	flush()
	return res
}
