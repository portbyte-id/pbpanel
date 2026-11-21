package sshauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ErrUnavailable is returned when the sshd journal cannot be read on this host:
// journalctl is missing, journald has no journal for the unit, or the read
// failed. The HTTP layer answers 503 for it.
var ErrUnavailable = errors.New("sshauth: journal unavailable")

// DefaultUnits are the units sshd's messages are filed under. Which one exists
// depends on the distro and on the OpenSSH version:
//
//	sshd.service          RHEL/Fedora/Rocky, and Debian's sshd only
//	ssh.service           Debian/Ubuntu
//	sshd-session.service  OpenSSH 9.8+, which split the per-connection
//	                      process into its own unit; Ubuntu 25.10+ uses it
//
// journalctl ORs several -u options, and a unit a host does not have is not an
// error, so asking for all three is how one query covers every supported host.
var DefaultUnits = []string{"sshd.service", "ssh.service", "sshd-session.service"}

// identifiers are the SYSLOG_IDENTIFIER values an sshd message may carry. A
// record with another identifier is not sshd's and is skipped even though a -u
// filter matched it.
var identifiers = map[string]bool{"sshd": true, "sshd-session": true, "sshd-auth": true, "ssh": true}

// unitPattern matches a systemd unit name. A unit is passed to journalctl as one
// argument, and nothing with a space, a path separator or a shell character is a
// unit name.
var unitPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@_.:-]{0,127}$`)

// Journal supplies the raw journal records of the sshd units. The records are
// what `journalctl -o json` writes: one JSON object per line, each carrying
// `__REALTIME_TIMESTAMP` and `MESSAGE`. Only as many as limit are returned.
//
// It is the same journal the log viewer streams, read with `-o json` instead of
// the viewer's `-o cat`: a record needs its own timestamp and identifier to be
// bounded to a window and attributed to a unit, and `-o cat` carries neither.
type Journal interface {
	Entries(ctx context.Context, units []string, since, until time.Time, limit int) ([]byte, error)
}

// Prober is a Journal that can report, before the first query, whether the host
// has a journal to read. *Journalctl implements it. A Journal without it is
// assumed usable and reports the failure on the first query.
type Prober interface {
	Probe(ctx context.Context) error
}

// Journalctl reads the journal through journalctl on this host.
type Journalctl struct {
	Run Runner
}

// Probe runs journalctl without a unit filter and asks for no records: it fails
// on a host without journalctl, with an unreadable journal, or outside systemd.
func (j Journalctl) Probe(ctx context.Context) error {
	out, err := j.Run.Run(ctx, "journalctl", "-n", "0", "--no-pager", "-q")
	if err != nil {
		return fmt.Errorf("%w: journalctl: %v: %s", ErrUnavailable, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Entries implements Journal.
func (j Journalctl) Entries(ctx context.Context, units []string, since, until time.Time, limit int) ([]byte, error) {
	args := []string{"--no-pager", "-o", "json", "-q",
		"--since", since.UTC().Format(time.RFC3339),
		"--until", until.UTC().Format(time.RFC3339),
		"-n", itoa(limit),
	}
	for _, u := range units {
		if !unitPattern.MatchString(u) {
			return nil, fmt.Errorf("%w: %q is not a unit name", ErrUnavailable, u)
		}
		args = append(args, "-u", u)
	}
	out, err := j.Run.Run(ctx, "journalctl", args...)
	if err != nil {
		return nil, fmt.Errorf("%w: journalctl: %v: %s", ErrUnavailable, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// Runner is the host command runner the package needs; *native.ExecRunner
// implements it.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

func itoa(n int) string {
	var b [20]byte
	i := len(b)
	if n == 0 {
		return "0"
	}
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}

// MaxRecord caps one journal record. A record longer than this is skipped: its
// MESSAGE is network-sourced text, and one line must not be able to pull an
// unbounded string into memory.
const MaxRecord = 1 << 20

// journalRecord is the part of a `journalctl -o json` line this package reads.
// MESSAGE is a string on most hosts and an array of bytes on some; both are
// read.
type journalRecord struct {
	Realtime  string          `json:"__REALTIME_TIMESTAMP"`
	Message   json.RawMessage `json:"MESSAGE"`
	Ident     string          `json:"SYSLOG_IDENTIFIER"`
	Unit      string          `json:"_SYSTEMD_UNIT"`
	Comm      string          `json:"_COMM"`
	Transport string          `json:"_TRANSPORT"`
}

// parseRecord reads one journal line into an Attempt. Anything that is not an
// sshd authentication attempt reports false.
func parseRecord(line []byte) (Attempt, bool) {
	var r journalRecord
	if err := json.Unmarshal(line, &r); err != nil {
		return Attempt{}, false
	}
	if r.Ident != "" && !identifiers[r.Ident] {
		return Attempt{}, false
	}
	// journalctl -o cat on Ubuntu prints the program tag; a MESSAGE captured
	// from a container or a piped stderr can carry it too.
	msg := strings.TrimSpace(string(messageText(r.Message)))
	msg = stripProgramTag(msg)
	a, ok := ParseMessage(msg)
	if !ok {
		return Attempt{}, false
	}
	at, ok := realtime(r.Realtime)
	if !ok {
		return Attempt{}, false
	}
	a.At = at
	return a, true
}

// messageText reads MESSAGE as a string, or as the byte array journald uses when
// the text is not valid UTF-8.
func messageText(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return []byte(s)
		}
		return nil
	}
	var b []byte
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	return nil
}

// programTag is the "sshd[1234]: " prefix a syslog line carries.
var programTag = regexp.MustCompile(`^[A-Za-z0-9._-]+\[[0-9]+\]: ?`)

func stripProgramTag(s string) string { return programTag.ReplaceAllString(s, "") }

// realtime reads __REALTIME_TIMESTAMP, which journald writes in microseconds
// since the epoch.
func realtime(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	var us int64
	if _, err := jsonNumber(v, &us); err != nil {
		return time.Time{}, false
	}
	return time.UnixMicro(us).UTC(), true
}

func jsonNumber(v string, out *int64) (bool, error) {
	var n int64
	for _, c := range v {
		if c < '0' || c > '9' {
			return false, errors.New("sshauth: not a number")
		}
		n = n*10 + int64(c-'0')
		if n < 0 {
			return false, errors.New("sshauth: number too large")
		}
	}
	*out = n
	return true, nil
}

// splitRecords yields the complete JSON lines of a journalctl output. A partial
// trailing line is dropped: limit may have cut a record in half.
func splitRecords(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSpace(b[:i])
		b = b[i+1:]
		if len(line) == 0 || len(line) > MaxRecord {
			continue
		}
		out = append(out, line)
	}
	return out
}
