package sysinfo

import (
	"os"
	"path/filepath"
)

// DiskUsage is statfs for one mount point, in bytes.
type DiskUsage struct {
	TotalBytes uint64
	// FreeBytes is the space an unprivileged process can still use, which is what
	// df reports as "Avail".
	FreeBytes uint64
	UsedBytes uint64
}

// Source is where a Sampler reads the host from. HostSource is the real one; tests
// substitute a fake serving fixture text. Keeping statfs behind this seam lets the
// package build and test on hosts that have no /proc.
type Source interface {
	// ReadProc returns the content of a file under /proc, named relative to it
	// ("stat", "meminfo", "net/dev", "mounts").
	ReadProc(name string) ([]byte, error)
	// Disk reports usage of the filesystem mounted at a path.
	Disk(mount string) (DiskUsage, error)
}

// SysSource is an optional second seam: a Source that also implements it can
// read files under /sys, which the disk-IO device mapping needs (LVM and
// dm-crypt names live in /sys/block/dm-N/dm/name). A Source without it simply
// has no IO figures for mapper devices.
type SysSource interface {
	ReadSys(name string) ([]byte, error)
}

// HostSource reads the running machine.
type HostSource struct {
	// ProcRoot is the procfs mount point. Empty means /proc.
	ProcRoot string
	// SysRoot is the sysfs mount point. Empty means /sys.
	SysRoot string
}

// ReadSys implements SysSource.
func (h HostSource) ReadSys(name string) ([]byte, error) {
	root := h.SysRoot
	if root == "" {
		root = "/sys"
	}
	return os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
}

// ReadProc implements Source.
func (h HostSource) ReadProc(name string) ([]byte, error) {
	root := h.ProcRoot
	if root == "" {
		root = "/proc"
	}
	return os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
}

// Disk implements Source. It is not available off Linux.
func (HostSource) Disk(mount string) (DiskUsage, error) { return statfs(mount) }
