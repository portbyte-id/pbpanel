package sshauth

import (
	"strings"
	"testing"
	"time"
)

// want is the part of an Attempt these tests compare; At is exercised by the
// journal tests.
type want struct {
	ip      string
	user    string
	port    int
	outcome Outcome
	method  Method
}

func (w want) check(t *testing.T, a Attempt) {
	t.Helper()
	if a.IP != w.ip || a.User != w.user || a.Port != w.port || a.Outcome != w.outcome || a.Method != w.method {
		t.Errorf("got %+v, want %+v", a, w)
	}
}

func TestParseMessage(t *testing.T) {
	for _, c := range []struct {
		name string
		msg  string
		want want
	}{
		{
			name: "accepted password",
			msg:  "Accepted password for deploy from 203.0.113.7 port 51234 ssh2",
			want: want{"203.0.113.7", "deploy", 51234, OutcomeSuccess, MethodPassword},
		},
		{
			name: "accepted publickey with key comment",
			msg:  "Accepted publickey for root from 198.51.100.4 port 4022 ssh2: ED25519 SHA256:abcdefg",
			want: want{"198.51.100.4", "root", 4022, OutcomeSuccess, MethodPublicKey},
		},
		{
			name: "accepted publickey without key comment",
			msg:  "Accepted publickey for root from 198.51.100.4 port 4022 ssh2",
			want: want{"198.51.100.4", "root", 4022, OutcomeSuccess, MethodPublicKey},
		},
		{
			name: "failed password",
			msg:  "Failed password for deploy from 203.0.113.7 port 51234 ssh2",
			want: want{"203.0.113.7", "deploy", 51234, OutcomeFailed, MethodPassword},
		},
		{
			name: "failed password for an invalid user",
			msg:  "Failed password for invalid user admin from 45.155.205.233 port 44556 ssh2",
			want: want{"45.155.205.233", "admin", 44556, OutcomeFailed, MethodPassword},
		},
		{
			name: "failed publickey",
			msg:  "Failed publickey for deploy from 203.0.113.7 port 51234 ssh2: RSA SHA256:xyz",
			want: want{"203.0.113.7", "deploy", 51234, OutcomeFailed, MethodPublicKey},
		},
		{
			name: "failed none",
			msg:  "Failed none for deploy from 203.0.113.7 port 51234 ssh2",
			want: want{"203.0.113.7", "deploy", 51234, OutcomeFailed, MethodNone},
		},
		{
			name: "invalid user prelude is not an attempt",
			msg:  "Invalid user admin from 45.155.205.233 port 44556",
			want: want{},
		},
		{
			name: "connection closed by anonymous client is not an attempt",
			msg:  "Connection closed by 45.155.205.233 port 44556 [preauth]",
			want: want{},
		},
		{
			name: "connection closed while authenticating",
			msg:  "Connection closed by authenticating user deploy 203.0.113.7 port 51234 [preauth]",
			want: want{"203.0.113.7", "deploy", 51234, OutcomeFailed, MethodNone},
		},
		{
			name: "connection closed for an invalid user",
			msg:  "Connection closed by invalid user admin 45.155.205.233 port 44556 [preauth]",
			want: want{"45.155.205.233", "admin", 44556, OutcomeFailed, MethodNone},
		},
		{
			name: "IPv6 address",
			msg:  "Failed password for deploy from 2001:db8::1 port 51234 ssh2",
			want: want{"2001:db8::1", "deploy", 51234, OutcomeFailed, MethodPassword},
		},
		{
			name: "IPv6 address in uppercase is canonicalised",
			msg:  "Accepted password for deploy from 2001:DB8::A port 51234 ssh2",
			want: want{"2001:db8::a", "deploy", 51234, OutcomeSuccess, MethodPassword},
		},
		{
			name: "IPv4-mapped IPv6",
			msg:  "Failed password for deploy from ::ffff:203.0.113.7 port 51234 ssh2",
			want: want{"::ffff:203.0.113.7", "deploy", 51234, OutcomeFailed, MethodPassword},
		},
		{
			name: "usernames with dots and dashes",
			msg:  "Accepted password for first.last-name from 203.0.113.7 port 51234 ssh2",
			want: want{"203.0.113.7", "first.last-name", 51234, OutcomeSuccess, MethodPassword},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, ok := ParseMessage(c.msg)
			if !ok != (c.want == want{}) {
				t.Fatalf("ok = %v, want %+v", ok, c.want)
			}
			if ok {
				c.want.check(t, a)
			}
		})
	}
}

// A record whose ip is not an IP literal is dropped: this field reaches JSON and
// a future firewall rule, and a hostname or a crafted string there is worse than
// a lost record.
func TestParseDropsNonAddressIPs(t *testing.T) {
	for _, msg := range []string{
		"Failed password for deploy from not-an-ip port 51234 ssh2",
		"Failed password for deploy from 203.0.113.7.example.com port 51234 ssh2",
		"Failed password for deploy from 999.1.1.1 port 51234 ssh2",
		"Failed password for deploy from 203.0.113.7/24 port 51234 ssh2",
		"Failed password for deploy from fe80::1%eth0 port 51234 ssh2",
		"Failed password for deploy from [2001:db8::1] port 51234 ssh2",
		"Accepted password for deploy from - port 51234 ssh2",
	} {
		if a, ok := ParseMessage(msg); ok {
			t.Errorf("%q parsed as %+v; want dropped", msg, a)
		}
	}
}

func TestParseDropsBadPorts(t *testing.T) {
	for _, msg := range []string{
		"Failed password for deploy from 203.0.113.7 port 0 ssh2",
		"Failed password for deploy from 203.0.113.7 port 70000 ssh2",
		"Failed password for deploy from 203.0.113.7 port -1 ssh2",
		"Failed password for deploy from 203.0.113.7 port abc ssh2",
		"Failed password for deploy from 203.0.113.7 port 051234 ssh2",
		"Failed password for deploy from 203.0.113.7 ssh2",
		"Failed password for deploy from 203.0.113.7",
	} {
		if a, ok := ParseMessage(msg); ok {
			t.Errorf("%q parsed as %+v; want dropped", msg, a)
		}
	}
}

// sshd writes more than authentication outcomes. None of it is an attempt, and a
// line this parser does not know must be skipped without an error.
func TestParseSkipsMessagesThatAreNotAttempts(t *testing.T) {
	for _, msg := range []string{
		"",
		"Accepted keyboard-interactive/pam for deploy from 203.0.113.7 port 51234 ssh2",
		"Failed gssapi-with-mic for deploy from 203.0.113.7 port 51234 ssh2",
		"Received disconnect from 203.0.113.7 port 51234:11: Bye Bye [preauth]",
		"Disconnected from authenticating user deploy 203.0.113.7 port 51234 [preauth]",
		"error: maximum authentication attempts exceeded for deploy from 203.0.113.7 port 51234 ssh2 [preauth]",
		"Server listening on 0.0.0.0 port 22.",
		"Connection from 203.0.113.7 port 51234 on 10.0.0.5 port 22",
		"PAM 2 more authentication failures; logname= uid=0 euid=0 tty=ssh ruser= rhost=203.0.113.7",
		// A name with a space cannot exist; the fields shift and it is dropped.
		"Failed password for invalid user admin;rm -rf / from 45.155.205.233 port 44556 ssh2",
		"garbage that is not an sshd message at all",
	} {
		if a, ok := ParseMessage(msg); ok {
			t.Errorf("%q parsed as %+v; want skipped", msg, a)
		}
	}
}

// The invalid-user prelude and its "Failed password for invalid user" line are
// one attempt, not two: the prelude is the line sshd writes first, and the pair
// always appears together.
func TestInvalidUserPairCountsOnce(t *testing.T) {
	lines := []string{
		"Invalid user admin from 45.155.205.233 port 44556",
		"Failed password for invalid user admin from 45.155.205.233 port 44556 ssh2",
	}
	var got int
	for _, l := range lines {
		if _, ok := ParseMessage(l); ok {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("the pair counted %d attempts, want 1", got)
	}
}

// The user name arrives from the network. It must survive as data, exactly as
// sent, and must never be interpreted: no shell is involved anywhere in this
// package, and the test pins the value the parser carries rather than a
// normalised one, so a later change cannot quietly start rewriting it.
//
// A name with a space is deliberately absent: sshd's line is space-delimited and
// no account name can contain one, so such a line is malformed and dropped (see
// TestParseSkipsMessagesThatAreNotAttempts).
func TestNetworkUsernameIsCarriedAsData(t *testing.T) {
	for _, name := range []string{
		`$(id)`,
		"`reboot`",
		"admin;rm-rf/",
		"a|b&c>d<e",
		"../etc/passwd",
		"$(curl${IFS}-s)",
	} {
		msg := "Failed password for invalid user " + name + " from 45.155.205.233 port 44556 ssh2"
		a, ok := ParseMessage(msg)
		if !ok {
			t.Fatalf("%q was dropped", name)
		}
		if a.User != name {
			t.Errorf("user = %q, want %q", a.User, name)
		}
		if a.IP != "45.155.205.233" || a.Port != 44556 {
			t.Errorf("fields = %+v", a)
		}
	}
}

// A control character in a network-sourced name must not reach a terminal or a
// JSON consumer.
func TestUsernameControlCharactersAreStripped(t *testing.T) {
	a, ok := ParseMessage("Failed password for invalid user ab\x1b[31mcd\x07 from 45.155.205.233 port 44556 ssh2")
	if !ok {
		t.Fatal("the line was dropped")
	}
	if a.User != "ab[31mcd" {
		t.Errorf("user = %q, want the escape bytes removed", a.User)
	}
}

func TestUsernameIsBounded(t *testing.T) {
	long := strings.Repeat("a", MaxUser+50)
	a, ok := ParseMessage("Failed password for invalid user " + long + " from 45.155.205.233 port 44556 ssh2")
	if !ok {
		t.Fatal("the line was dropped")
	}
	if len(a.User) != MaxUser {
		t.Errorf("user length = %d, want %d", len(a.User), MaxUser)
	}
}

// Interleaved and half-written lines are what a journal read under load looks
// like. Each is skipped on its own; one bad line never hides the good ones.
func TestParseToleratesInterleavedAndPartialLines(t *testing.T) {
	lines := []string{
		"Failed password for deploy from 203.0.113.7 port 1 ssh2",
		"Failed password for deploy from 203.0.113.7 port ",
		"Invalid user x from 203.0.113.7 port 2",
		"Accepted password for deploy from 203.0.113.7 port 3 ssh2",
		"Failed password for deploy from",
		"Failed  from 203.0.113.7 port 4 ssh2",
	}
	var got []int
	for _, l := range lines {
		if a, ok := ParseMessage(l); ok {
			got = append(got, a.Port)
		}
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("ports = %v, want [1 3]", got)
	}
}

// The timestamp comes from the journal, in microseconds since the epoch.
func TestRealtimeReadsMicroseconds(t *testing.T) {
	at, ok := realtime("1700000000123456")
	if !ok {
		t.Fatal("not read")
	}
	want := time.Date(2023, 11, 14, 22, 13, 20, 123456000, time.UTC)
	if !at.Equal(want) {
		t.Errorf("at = %v, want %v", at, want)
	}
	for _, v := range []string{"", "x", "-1", "1.5", "9999999999999999999999"} {
		if _, ok := realtime(v); ok {
			t.Errorf("%q accepted", v)
		}
	}
}
