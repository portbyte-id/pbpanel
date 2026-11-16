//go:build linux

package main

import "golang.org/x/sys/unix"

// The terminal ioctl numbers. Linux uses the TCSETS family; the BSDs use the
// TIOCSETAF family, so the two cannot share a constant and are split by build
// tag rather than guessed at runtime.
const (
	termiosRead  = unix.TCGETS
	termiosWrite = unix.TCSETS
)
