//go:build linux || freebsd || netbsd || openbsd || darwin

package main

import (
	"bufio"
	"strings"
	"testing"
)

// TestReadLine pins what readPassword depends on: one line, terminator gone, a
// CRLF cleaned up, a last line without a newline still delivered, and nothing at
// all reported as no line rather than as an empty password.
func TestReadLine(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  string
		want   string
		wantOK bool
	}{
		{"a normal line", "hunter2\n", "hunter2", true},
		{"a CRLF line", "hunter2\r\n", "hunter2", true},
		{"no trailing newline", "hunter2", "hunter2", true},
		{"empty input at EOF", "", "", false},
		// An empty line is a line the operator typed, not a missing one: the
		// caller has to be able to tell the two apart.
		{"an empty line", "\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := readLine(strings.NewReader(tc.input))
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("readLine(%q) = %q, %v; want %q, %v", tc.input, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestReadLineSharesOneBufferedReader reads twice through one buffered reader, the
// way readPassword does through stdinLines. A reader created per prompt would take
// in more than its own line and leave the second prompt with nothing, which is
// what install's password confirmation would then see.
func TestReadLineSharesOneBufferedReader(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("first\nsecond\n"))
	if got, ok := readLine(r); got != "first" || !ok {
		t.Fatalf("first readLine = %q, %v", got, ok)
	}
	if got, ok := readLine(r); got != "second" || !ok {
		t.Fatalf("second readLine = %q, %v", got, ok)
	}
	if _, ok := readLine(r); ok {
		t.Error("a third readLine read a line from an exhausted reader")
	}
}
