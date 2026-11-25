//go:build linux || freebsd || netbsd || openbsd || darwin

package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// stdinLines buffers stdin for every prompt. It is one reader for the whole
// process on purpose: a buffered reader takes in more than the line it returns, so
// a fresh one per prompt would swallow the answer to the next prompt and report
// EOF. install asks for a password twice (the confirmation) and reads both
// through this.
var stdinLines = bufio.NewReader(os.Stdin)

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

	return readLine(stdinLines)
}

// readLine reads one line from r, without its terminator. A trailing CR is
// dropped as well, so a line ending in CRLF reads the same as one ending in LF.
//
// It reads a byte at a time rather than asking for a whole line, so no input past
// the newline is consumed on the caller's behalf: a reader that already buffers
// keeps the rest for the next call.
//
// A reader with nothing left returns ok=false, which is what a blank piped stdin
// does. A last line with no newline is still a line.
func readLine(r io.Reader) (string, bool) {
	br := bufio.NewReader(r)
	var line []byte
	for {
		b, err := br.ReadByte()
		if err != nil {
			// Anything other than EOF means the stream broke; what was read is
			// still a line if there is anything, and otherwise there is none.
			if len(line) == 0 && errors.Is(err, io.EOF) {
				return "", false
			}
			break
		}
		if b == '\n' {
			break
		}
		line = append(line, b)
	}
	return strings.TrimSuffix(string(line), "\r"), true
}

// readLine reads one line, without its newline, from r.
func readLine(r io.Reader) (string, bool) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimRight(line, "\r\n"), true
}
