package sshauth

import (
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Outcome is how an attempt ended.
type Outcome string

const (
	OutcomeFailed  Outcome = "failed"
	OutcomeSuccess Outcome = "success"
)

// Method is the authentication method an attempt used. An attempt that ended
// before a method was chosen (a client that disconnected during the banner or
// user-name stage) is MethodNone.
type Method string

const (
	MethodPassword  Method = "password"
	MethodPublicKey Method = "publickey"
	MethodNone      Method = "none"
)

// Attempt is one SSH authentication attempt, as sshd logged it.
//
// User is text that arrived from the network on sshd's own line (an "invalid
// user" string is whatever the client sent). It is data: it is never an argv, a
// path or a shell input, and it leaves this package only inside JSON.
type Attempt struct {
	At      time.Time `json:"at"`
	IP      string    `json:"ip"`
	User    string    `json:"user"`
	Port    int       `json:"port"`
	Outcome Outcome   `json:"outcome"`
	Method  Method    `json:"method"`
}

// MaxUser caps the account name copied out of a line. sshd truncates the name it
// logs for an invalid user, but a line is still network-sourced text; anything
// longer is cut so one message cannot carry an unbounded field into a response.
const MaxUser = 128

// message kinds after the program tag ("sshd[1234]: ") has been stripped.
const (
	prefixAccepted   = "Accepted "
	prefixFailed     = "Failed "
	prefixInvalid    = "Invalid user "
	prefixDisconnect = "Connection closed by authenticating user "
	// invalidDisconnect is the same line for a name sshd did not find.
	invalidDisconnect = "Connection closed by invalid user "
)

// ParseMessage parses one sshd journal message: the text after
// "sshd[<pid>]: ", which the caller has already cut off.
//
// Recognised shapes (sshd's own auth.c / auth2-*.c log lines):
//
//	Accepted password for <user> from <ip> port <n> ssh2
//	Accepted publickey for <user> from <ip> port <n> ssh2: RSA SHA256:...
//	Accepted publickey for <user> from <ip> port <n> ssh2
//	Failed password for <user> from <ip> port <n> ssh2
//	Failed password for invalid user <user> from <ip> port <n> ssh2
//	Failed publickey for <user> from <ip> port <n> ssh2: RSA SHA256:...
//	Failed none for <user> from <ip> port <n> ssh2
//	Invalid user <user> from <ip> port <n>
//	Connection closed by authenticating user <user> <ip> port <n> [preauth]
//	Connection closed by invalid user <user> <ip> port <n> [preauth]
//	Connection closed by <ip> port <n> [preauth]
//
// "Invalid user <u> from <ip> port <n>" is a prelude sshd writes when the name
// is unknown, and the very next line is normally the "Failed password for
// invalid user <u> from ..." that counts the attempt. The prelude therefore
// reports false (it is not an attempt) so the pair counts once. That is only
// safe because a failed attempt against an unknown name always logs both lines
// together; there is no case where the prelude stands alone as the record of an
// attempt.
//
// A "Connection closed by authenticating user" line is a client that gave up
// (or was dropped) while sshd was still asking for credentials. It is an attempt
// — something tried and did not get in — so it is reported as failed with
// MethodNone: no method had been chosen yet, and calling it "password" would be
// a guess. Its sibling "Connection closed by <ip> port <n>" carries no user name
// at all and is not an authentication attempt (often a TLS probe or a scanner),
// so it reports false.
//
// A record whose "ip" is not an IP literal reports false: this field is copied
// into a JSON response and compared against firewall rules, and a hostname or a
// crafted string there is worse than a dropped record.
//
// The timestamp is not in the message; the caller sets At from the journal.
//
// Fields are split on single spaces, which is sshd's own grammar. An account
// name cannot contain a space, so a name that does is not one sshd could log:
// the words of such a line are read as the next fields and the line is dropped
// as malformed, which is the intended outcome.
func ParseMessage(msg string) (Attempt, bool) {
	// A journal record holds one message; a stray newline (a writer that logged
	// a multi-line string) ends it, and only the sshd line is an attempt.
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	switch {
	case strings.HasPrefix(msg, prefixDisconnect):
		return parseConnectionClosed(msg, prefixDisconnect)
	case strings.HasPrefix(msg, invalidDisconnect):
		return parseConnectionClosed(msg, invalidDisconnect)
	}
	var a Attempt
	var rest string
	switch {
	case strings.HasPrefix(msg, prefixAccepted):
		a.Outcome = OutcomeSuccess
		rest = msg[len(prefixAccepted):]
	case strings.HasPrefix(msg, prefixFailed):
		a.Outcome = OutcomeFailed
		rest = msg[len(prefixFailed):]
	case strings.HasPrefix(msg, prefixInvalid):
		// The prelude to a "Failed password for invalid user" line; see above.
		return Attempt{}, false
	default:
		return Attempt{}, false
	}

	// "<method> for [invalid user ]<user> from <ip> port <n>[ ssh2][: key]"
	method, rest, ok := cutWord(rest)
	if !ok {
		return Attempt{}, false
	}
	switch method {
	case "password":
		a.Method = MethodPassword
	case "publickey":
		a.Method = MethodPublicKey
	case "none":
		a.Method = MethodNone
	default:
		// "Failed keyboard-interactive", "Accepted gssapi-with-mic" and anything
		// sshd may add later. The task names three methods; these are not them,
		// so the line is skipped rather than mislabelled.
		return Attempt{}, false
	}
	const forWord = "for "
	if !strings.HasPrefix(rest, forWord) {
		return Attempt{}, false
	}
	rest = rest[len(forWord):]
	rest = strings.TrimPrefix(rest, "invalid user ")

	user, rest, ok := cutWord(rest)
	if !ok || user == "" {
		return Attempt{}, false
	}
	// cutWord left rest at the "from" keyword of "<user> from <ip> port <n>".
	rest, ok = strings.CutPrefix(rest, "from ")
	if !ok {
		return Attempt{}, false
	}
	ip, rest, ok := cutWord(rest)
	if !ok {
		return Attempt{}, false
	}
	rest, ok = strings.CutPrefix(rest, "port ")
	if !ok {
		return Attempt{}, false
	}
	portTok, _, ok := cutWord(rest)
	if !ok {
		return Attempt{}, false
	}
	// A trailing "ssh2" or ": RSA SHA256:..." is not needed: the split above
	// already stopped at the first space after the port.
	addr, ok := parseAddr(ip)
	if !ok {
		return Attempt{}, false
	}
	port, ok := parsePort(portTok)
	if !ok {
		return Attempt{}, false
	}
	a.User, a.IP, a.Port = clipUser(user), addr, port
	return a, true
}

// parseConnectionClosed reads
//
//	Connection closed by authenticating user <user> <ip> port <n> [preauth]
//	Connection closed by invalid user <user> <ip> port <n> [preauth]
//
// prefix is the matched one; both carry the user name after it.
func parseConnectionClosed(msg, prefix string) (Attempt, bool) {
	rest := msg[len(prefix):]
	user, rest, ok := cutWord(rest)
	if !ok || user == "" {
		return Attempt{}, false
	}
	ip, rest, ok := cutWord(rest)
	if !ok {
		return Attempt{}, false
	}
	rest, ok = strings.CutPrefix(rest, "port ")
	if !ok {
		return Attempt{}, false
	}
	portTok, _, ok := cutWord(rest)
	if !ok {
		return Attempt{}, false
	}
	addr, ok := parseAddr(ip)
	if !ok {
		return Attempt{}, false
	}
	port, ok := parsePort(portTok)
	if !ok {
		return Attempt{}, false
	}
	return Attempt{
		IP: strings.ToLower(addr), User: clipUser(user), Port: port,
		Outcome: OutcomeFailed, Method: MethodNone,
	}, true
}

// cutWord splits off the first space-delimited word.
func cutWord(s string) (word, rest string, ok bool) {
	word, rest, ok = strings.Cut(s, " ")
	return word, rest, ok
}

// parseAddr accepts an IPv4 or IPv6 literal and reports it in its canonical
// form. An address with a zone ("fe80::1%eth0") is rejected: sshd never logs
// one, and the zone names a local interface, which is not a client.
func parseAddr(s string) (string, bool) {
	if strings.ContainsRune(s, '%') {
		return "", false
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return "", false
	}
	return a.String(), true
}

// parsePort reads a port in 1..65535. sshd logs the client's source port, and 0
// is not one.
func parsePort(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}

// clipUser bounds a network-sourced name and removes anything that is not
// printable ASCII or a space, so a terminal or a JSON consumer cannot be made to
// interpret control characters.
func clipUser(s string) string {
	if len(s) > MaxUser {
		s = s[:MaxUser]
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}
