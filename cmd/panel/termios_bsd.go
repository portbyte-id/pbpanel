//go:build darwin || freebsd || netbsd || openbsd

package main

import "golang.org/x/sys/unix"

// The BSDs take the TIOCGETA/TIOCSETA pair where Linux takes TCSETS.
const (
	termiosRead  = unix.TIOCGETA
	termiosWrite = unix.TIOCSETA
)
