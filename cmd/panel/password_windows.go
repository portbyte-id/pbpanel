//go:build windows

package main

// readPassword is the Windows stub. The panel only installs on Linux: `panel
// install` refuses on any distribution osdep does not implement, and the tests
// that exercise the prompt run on Linux. On Windows there is no terminal to turn
// echo off on through x/sys/unix, so the prompt reports "not a terminal" and
// install generates the password instead, which is the same path a piped install
// takes.
func readPassword() (string, bool) { return "", false }
