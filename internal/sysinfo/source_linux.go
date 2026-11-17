//go:build linux

package sysinfo

import "syscall"

func statfs(path string) (DiskUsage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return DiskUsage{}, err
	}
	bs := uint64(st.Bsize) //nolint:unconvert // Bsize is int32 or int64 by architecture
	total := uint64(st.Blocks) * bs
	free := uint64(st.Bfree) * bs
	return DiskUsage{TotalBytes: total, FreeBytes: uint64(st.Bavail) * bs, UsedBytes: total - min(free, total)}, nil
}
