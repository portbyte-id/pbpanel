//go:build !linux

package sysinfo

import "errors"

// ErrUnsupported is returned by HostSource.Disk on a host without statfs.
var ErrUnsupported = errors.New("sysinfo: disk usage is only available on Linux")

func statfs(string) (DiskUsage, error) { return DiskUsage{}, ErrUnsupported }
