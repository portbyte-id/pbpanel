package sysinfo

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CPUTimes is one "cpu" line of /proc/stat, in clock ticks since boot.
type CPUTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

// Total is every tick that passed. Guest time is already counted inside User and
// Nice by the kernel, so it is not added again.
func (c CPUTimes) Total() uint64 {
	return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal
}

// IdleTotal is the ticks the CPU spent waiting for work, including I/O wait.
func (c CPUTimes) IdleTotal() uint64 { return c.Idle + c.IOWait }

// CPUPercent is the busy share, 0-100, between two readings of the same CPU. It is
// 0 when no ticks passed or a counter went backwards (a reboot between readings).
func CPUPercent(prev, cur CPUTimes) float64 {
	if cur.Total() <= prev.Total() || cur.IdleTotal() < prev.IdleTotal() {
		return 0
	}
	total := float64(cur.Total() - prev.Total())
	idle := float64(cur.IdleTotal() - prev.IdleTotal())
	return clampPercent((total - idle) / total * 100)
}

// ParseCPUStat reads /proc/stat. It returns the aggregate line and one entry per
// core, in core order. Kernels older than 2.6.33 omit the trailing columns; a
// missing column counts as zero.
func ParseCPUStat(data []byte) (total CPUTimes, cores []CPUTimes, err error) {
	found := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		t, err := parseCPULine(f[1:])
		if err != nil {
			return CPUTimes{}, nil, fmt.Errorf("sysinfo: /proc/stat %s: %w", f[0], err)
		}
		if f[0] == "cpu" {
			total, found = t, true
			continue
		}
		cores = append(cores, t)
	}
	if err := sc.Err(); err != nil {
		return CPUTimes{}, nil, fmt.Errorf("sysinfo: /proc/stat: %w", err)
	}
	if !found {
		return CPUTimes{}, nil, fmt.Errorf("sysinfo: /proc/stat has no cpu line")
	}
	return total, cores, nil
}

func parseCPULine(f []string) (CPUTimes, error) {
	if len(f) < 4 {
		return CPUTimes{}, fmt.Errorf("want at least 4 columns, got %d", len(f))
	}
	var v [8]uint64
	for i := 0; i < len(v) && i < len(f); i++ {
		n, err := strconv.ParseUint(f[i], 10, 64)
		if err != nil {
			return CPUTimes{}, err
		}
		v[i] = n
	}
	return CPUTimes{User: v[0], Nice: v[1], System: v[2], Idle: v[3], IOWait: v[4], IRQ: v[5], SoftIRQ: v[6], Steal: v[7]}, nil
}

// Load is /proc/loadavg.
type Load struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
	// Running is the number of runnable scheduling entities right now.
	Running int `json:"running"`
	// Total is the number of scheduling entities that exist.
	Total int `json:"total"`
}

// ParseLoadavg reads /proc/loadavg: "0.52 0.58 0.59 1/467 12345".
func ParseLoadavg(data []byte) (Load, error) {
	f := strings.Fields(string(data))
	if len(f) < 4 {
		return Load{}, fmt.Errorf("sysinfo: /proc/loadavg: want 4 fields, got %d", len(f))
	}
	var l Load
	var err error
	for i, dst := range []*float64{&l.One, &l.Five, &l.Fifteen} {
		if *dst, err = strconv.ParseFloat(f[i], 64); err != nil {
			return Load{}, fmt.Errorf("sysinfo: /proc/loadavg: %w", err)
		}
	}
	run, tot, ok := strings.Cut(f[3], "/")
	if !ok {
		return Load{}, fmt.Errorf("sysinfo: /proc/loadavg: bad runnable field %q", f[3])
	}
	if l.Running, err = strconv.Atoi(run); err != nil {
		return Load{}, fmt.Errorf("sysinfo: /proc/loadavg: %w", err)
	}
	if l.Total, err = strconv.Atoi(tot); err != nil {
		return Load{}, fmt.Errorf("sysinfo: /proc/loadavg: %w", err)
	}
	return l, nil
}

// Memory is /proc/meminfo reduced to what the dashboard shows. Every field is in
// bytes (meminfo reports kibibytes).
type Memory struct {
	TotalBytes uint64 `json:"total_bytes"`
	// AvailableBytes is what a new workload can use without swapping. Kernels
	// before 3.14 have no MemAvailable, so it is estimated as free + buffers +
	// cached.
	AvailableBytes uint64 `json:"available_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	FreeBytes      uint64 `json:"free_bytes"`
	BuffersBytes   uint64 `json:"buffers_bytes"`
	CachedBytes    uint64 `json:"cached_bytes"`
	SwapTotalBytes uint64 `json:"swap_total_bytes"`
	SwapFreeBytes  uint64 `json:"swap_free_bytes"`
	SwapUsedBytes  uint64 `json:"swap_used_bytes"`
}

// ParseMeminfo reads /proc/meminfo.
func ParseMeminfo(data []byte) (Memory, error) {
	kv := map[string]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && strings.EqualFold(f[1], "kB") {
			n *= 1024
		}
		kv[key] = n
	}
	if err := sc.Err(); err != nil {
		return Memory{}, fmt.Errorf("sysinfo: /proc/meminfo: %w", err)
	}
	total, ok := kv["MemTotal"]
	if !ok || total == 0 {
		return Memory{}, fmt.Errorf("sysinfo: /proc/meminfo has no MemTotal")
	}
	m := Memory{
		TotalBytes: total, FreeBytes: kv["MemFree"], BuffersBytes: kv["Buffers"], CachedBytes: kv["Cached"],
		SwapTotalBytes: kv["SwapTotal"], SwapFreeBytes: kv["SwapFree"],
	}
	if avail, ok := kv["MemAvailable"]; ok {
		m.AvailableBytes = avail
	} else {
		m.AvailableBytes = m.FreeBytes + m.BuffersBytes + m.CachedBytes
	}
	m.AvailableBytes = min(m.AvailableBytes, total)
	m.UsedBytes = total - m.AvailableBytes
	if m.SwapTotalBytes > 0 {
		m.SwapUsedBytes = m.SwapTotalBytes - min(m.SwapFreeBytes, m.SwapTotalBytes)
	}
	return m, nil
}

// ParseUptime reads /proc/uptime and returns the time since boot. The idle column
// is ignored.
func ParseUptime(data []byte) (time.Duration, error) {
	f := strings.Fields(string(data))
	if len(f) < 1 {
		return 0, fmt.Errorf("sysinfo: /proc/uptime is empty")
	}
	secs, err := strconv.ParseFloat(f[0], 64)
	if err != nil || secs < 0 {
		return 0, fmt.Errorf("sysinfo: /proc/uptime: bad value %q", f[0])
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// NetCounters is one interface of /proc/net/dev: totals since the interface
// appeared.
type NetCounters struct {
	Iface     string
	RxBytes   uint64
	RxPackets uint64
	RxErrors  uint64
	TxBytes   uint64
	TxPackets uint64
	TxErrors  uint64
}

// ParseNetDev reads /proc/net/dev. The loopback interface is returned like any
// other; the sampler is what leaves it out.
func ParseNetDev(data []byte) ([]NetCounters, error) {
	var out []NetCounters
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue // the two header lines have no colon
		}
		f := strings.Fields(rest)
		if len(f) < 16 {
			return nil, fmt.Errorf("sysinfo: /proc/net/dev %q: want 16 columns, got %d", strings.TrimSpace(name), len(f))
		}
		var v [16]uint64
		for i := range v {
			n, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("sysinfo: /proc/net/dev %q: %w", strings.TrimSpace(name), err)
			}
			v[i] = n
		}
		out = append(out, NetCounters{
			Iface: strings.TrimSpace(name), RxBytes: v[0], RxPackets: v[1], RxErrors: v[2],
			TxBytes: v[8], TxPackets: v[9], TxErrors: v[10],
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("sysinfo: /proc/net/dev: %w", err)
	}
	return out, nil
}

// Mount is one /proc/mounts entry.
type Mount struct {
	Device string
	Point  string
	FSType string
}

// ParseMounts reads /proc/mounts, decoding the octal escapes the kernel uses for
// spaces, tabs, newlines and backslashes in a path.
func ParseMounts(data []byte) ([]Mount, error) {
	var out []Mount
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if len(f) < 3 {
			return nil, fmt.Errorf("sysinfo: /proc/mounts: short line %q", sc.Text())
		}
		out = append(out, Mount{Device: unescapeMount(f[0]), Point: unescapeMount(f[1]), FSType: f[2]})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("sysinfo: /proc/mounts: %w", err)
	}
	return out, nil
}

func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// diskFSTypes are the filesystems backed by a block device the operator cares
// about. Pseudo filesystems, overlay (Docker layers), squashfs (snaps) and network
// mounts (statfs on a dead NFS server blocks) are left out on purpose.
var diskFSTypes = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true,
	"f2fs": true, "vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "fuseblk": true,
	"reiserfs": true, "jfs": true,
}

// DiskMounts keeps the entries worth reporting as a disk: one per block device
// (the first mount point wins, so a bind mount of the same device is not counted
// twice), in /proc/mounts order.
func DiskMounts(all []Mount) []Mount {
	seen := map[string]bool{}
	var out []Mount
	for _, m := range all {
		if !diskFSTypes[m.FSType] || seen[m.Device] {
			continue
		}
		seen[m.Device] = true
		out = append(out, m)
	}
	return out
}

// DiskCounters is one device of /proc/diskstats: sectors transferred since boot.
type DiskCounters struct {
	Name         string
	SectorsRead  uint64
	SectorsWrite uint64
}

// SectorBytes is the unit /proc/diskstats counts in, whatever the device's real
// sector size is.
const SectorBytes = 512

// ParseDiskstats reads /proc/diskstats: "8 1 sda1 reads merged sectors ms
// writes merged sectors ms ...". Lines too short to carry the write sectors
// (very old kernels, partitions on some 2.4-era layouts) are skipped.
func ParseDiskstats(data []byte) (map[string]DiskCounters, error) {
	out := map[string]DiskCounters{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if len(f) < 10 {
			continue
		}
		rd, err := strconv.ParseUint(f[5], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("sysinfo: /proc/diskstats %s: %w", f[2], err)
		}
		wr, err := strconv.ParseUint(f[9], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("sysinfo: /proc/diskstats %s: %w", f[2], err)
		}
		out[f[2]] = DiskCounters{Name: f[2], SectorsRead: rd, SectorsWrite: wr}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("sysinfo: /proc/diskstats: %w", err)
	}
	return out, nil
}

// DiskstatsName maps a /proc/mounts device to its /proc/diskstats name. dmNames
// maps a device-mapper name ("vg0-root") to its kernel name ("dm-0"); it may be
// nil. ok is false when the device has no diskstats entry that can be told from
// the mount alone (by-uuid links, /dev/root, ZFS datasets, overlay).
func DiskstatsName(device string, dmNames map[string]string, known map[string]DiskCounters) (string, bool) {
	var name string
	switch {
	case strings.HasPrefix(device, "/dev/mapper/"):
		n, ok := dmNames[strings.TrimPrefix(device, "/dev/mapper/")]
		if !ok {
			return "", false
		}
		name = n
	case strings.HasPrefix(device, "/dev/") && !strings.Contains(device[len("/dev/"):], "/"):
		name = strings.TrimPrefix(device, "/dev/")
	default:
		return "", false
	}
	if _, ok := known[name]; !ok {
		return "", false
	}
	return name, true
}

func clampPercent(p float64) float64 {
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	}
	return p
}
