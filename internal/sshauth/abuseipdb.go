package sshauth

import (
	"encoding/csv"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"time"
)

// AbuseIPDB bulk-report categories: 18 is Brute-Force and 22 is SSH. A failed
// password guess against sshd is both, so every row carries the pair.
const abuseIPDBCategories = "18,22"

// abuseIPDBCommentMax keeps a comment inside the length AbuseIPDB accepts for a
// report. The usernames are what grows it, so they are trimmed first.
const abuseIPDBCommentMax = 1000

// abuseIPDBUsersShown is how many distinct usernames a comment names before it
// says how many more there were.
const abuseIPDBUsersShown = 10

// abuseIPDBHeader is the column line AbuseIPDB's bulk CSV upload expects.
var abuseIPDBHeader = []string{"IP", "Categories", "ReportDate", "Comment"}

// WriteAbuseIPDB writes the failed attempts as an AbuseIPDB bulk-report CSV: one
// row per source address, not one per attempt, since AbuseIPDB refuses a second
// report for the same address in a short span and a row per probe would be
// rejected in bulk. The row carries the address's latest attempt as its date and
// a comment with the count, the first and last time, and the usernames tried.
//
// Addresses that cannot be reported (private, loopback, link-local, or not an
// address at all) are left out, since AbuseIPDB rejects them. The rows are
// ordered by attempt count, most first, then by address, so the file is stable.
func WriteAbuseIPDB(w io.Writer, failed []Attempt) error {
	type group struct {
		ip          string
		count       int
		first, last time.Time
		users       []string
		seenUser    map[string]bool
		extraUsers  int
	}
	groups := map[string]*group{}
	for _, a := range failed {
		if a.Outcome != OutcomeFailed || !reportable(a.IP) {
			continue
		}
		g := groups[a.IP]
		if g == nil {
			g = &group{ip: a.IP, first: a.At, last: a.At, seenUser: map[string]bool{}}
			groups[a.IP] = g
		}
		g.count++
		if a.At.Before(g.first) {
			g.first = a.At
		}
		if a.At.After(g.last) {
			g.last = a.At
		}
		if a.User == "" || g.seenUser[a.User] {
			continue
		}
		g.seenUser[a.User] = true
		if len(g.users) < abuseIPDBUsersShown {
			g.users = append(g.users, a.User)
		} else {
			g.extraUsers++
		}
	}

	rows := make([]*group, 0, len(groups))
	for _, g := range groups {
		rows = append(rows, g)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].ip < rows[j].ip
	})

	cw := csv.NewWriter(w)
	if err := cw.Write(abuseIPDBHeader); err != nil {
		return err
	}
	for _, g := range rows {
		if err := cw.Write([]string{
			g.ip,
			abuseIPDBCategories,
			g.last.UTC().Format(time.RFC3339),
			abuseIPDBComment(g.count, g.first, g.last, g.users, g.extraUsers),
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// reportable reports whether an address is a public one AbuseIPDB will accept.
func reportable(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsUnspecified() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast()
}

// abuseIPDBComment is the plain-text note on one row. It names the usernames
// tried so the report can be checked, and it is cut to abuseIPDBCommentMax runes.
func abuseIPDBComment(count int, first, last time.Time, users []string, extra int) string {
	var b strings.Builder
	noun := "attempts"
	if count == 1 {
		noun = "attempt"
	}
	fmt.Fprintf(&b, "SSH login failed: %d %s between %s and %s UTC",
		count, noun, first.UTC().Format("2006-01-02 15:04:05"), last.UTC().Format("2006-01-02 15:04:05"))
	if len(users) > 0 {
		b.WriteString("; usernames tried: ")
		b.WriteString(strings.Join(users, ", "))
		if extra > 0 {
			fmt.Fprintf(&b, " and %d more", extra)
		}
	}
	return truncateRunes(b.String(), abuseIPDBCommentMax)
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
