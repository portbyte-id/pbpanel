//go:build linux || freebsd || netbsd || openbsd || darwin

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// readPassword reads one line from stdin with echo turned off, so a password
// typed at the console is not left on the terminal or in the scrollback.
//
// This is the part of golang.org/x/term's ReadPassword that install needs: check
// that stdin is a terminal, turn ECHO off, read a line, restore the terminal even
// on error. The panel already depends on golang.org/x/sys, and adding a module
// for one function would be a dependency for twenty lines of ioctl.
//
// A non-terminal returns ok=false without reading, which is what makes a piped
// or scripted install fall through to the generated password instead of
// blocking.
func readPassword() (string, bool) {
	fd := int(os.Stdin.Fd())

	saved, err := unix.IoctlGetTermios(fd, termiosRead)
	if err != nil {
		return "", false
	}

	echoOff := *saved
	echoOff.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, termiosWrite, &echoOff); err != nil {
		return "", false
	}
	defer func() {
		// Restore the state the caller's shell had, not one computed here.
		_ = unix.IoctlSetTermios(fd, termiosWrite, saved)
	}()

	return readLine(os.Stdin)
}
