package wafmon

import (
	"encoding/csv"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"time"
)

// abuseIPDBWebAttack is the AbuseIPDB category for a web application attack, which
// is what a ModSecurity match is.
const abuseIPDBWebAttack = "21"

// abuseIPDBCommentMax keeps a comment inside the length AbuseIPDB accepts for a
// report.
const abuseIPDBCommentMax = 1000

// abuseIPDBRulesShown is how many rules a comment names before it says how many
// more there were.
const abuseIPDBRulesShown = 3

// abuseIPDBHeader is the column line AbuseIPDB's bulk CSV upload expects.
var abuseIPDBHeader = []string{"IP", "Categories", "ReportDate", "Comment"}

// Matching returns every event the query matches, newest first. Before and Limit
// are ignored: an export covers the whole filtered set, not one page of it.
func (c *Collector) Matching(q EventQuery) []Event {
	out := []Event{}
	c.mu.RLock()
	defer c.mu.RUnlock()
	c.events.newestFirst(func(e Event) bool {
		if q.match(e) {
			out = append(out, e)
		}
		return true
	})
	return out
}

// WriteAbuseIPDB writes WAF events as an AbuseIPDB bulk-report CSV: one row per
// source address, not one per request, since AbuseIPDB refuses a second report for
// the same address in a short span. The row carries the address's latest event as
// its date, and a comment with the request count, what the WAF did, the window,
// the sites hit and the rules that matched.
//
// Addresses AbuseIPDB will not accept (private, loopback, link-local, or not an
// address at all) are left out. Rows are ordered by request count, most first,
// then by address, so the file is stable.
func WriteAbuseIPDB(w io.Writer, events []Event) error {
	type rule struct {
		id, message string
		count       int
	}
	type group struct {
		ip             string
		count, blocked int
		first, last    time.Time
		sites          map[string]bool
		rules          map[string]*rule
	}
	groups := map[string]*group{}
	for _, e := range events {
		if !reportable(e.IP) {
			continue
		}
		g := groups[e.IP]
		if g == nil {
			g = &group{ip: e.IP, first: e.Time, last: e.Time, sites: map[string]bool{}, rules: map[string]*rule{}}
			groups[e.IP] = g
		}
		g.count++
		if e.Action == ActionBlocked {
			g.blocked++
		}
		if e.Time.Before(g.first) {
			g.first = e.Time
		}
		if e.Time.After(g.last) {
			g.last = e.Time
		}
		if e.Site != "" {
			g.sites[e.Site] = true
		}
		hits := e.Matches
		if len(hits) == 0 && e.RuleID != "" {
			hits = []Match{{RuleID: e.RuleID, Message: e.Message}}
		}
		for _, m := range hits {
			// The anomaly-score rules say that a request scored high, not what it was.
			if m.RuleID == "" || isAnomalyRule(m.RuleID) {
				continue
			}
			r := g.rules[m.RuleID]
			if r == nil {
				r = &rule{id: m.RuleID, message: m.Message}
				g.rules[m.RuleID] = r
			}
			r.count++
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
		rules := make([]*rule, 0, len(g.rules))
		for _, r := range g.rules {
			rules = append(rules, r)
		}
		sort.Slice(rules, func(i, j int) bool {
			if rules[i].count != rules[j].count {
				return rules[i].count > rules[j].count
			}
			return rules[i].id < rules[j].id
		})
		var b strings.Builder
		fmt.Fprintf(&b, "ModSecurity WAF: %d %s (%d blocked, %d detected) between %s and %s UTC",
			g.count, plural(g.count, "request", "requests"), g.blocked, g.count-g.blocked,
			g.first.UTC().Format("2006-01-02 15:04:05"), g.last.UTC().Format("2006-01-02 15:04:05"))
		if len(g.sites) > 0 {
			names := make([]string, 0, len(g.sites))
			for s := range g.sites {
				names = append(names, s)
			}
			sort.Strings(names)
			b.WriteString("; sites: ")
			b.WriteString(strings.Join(names, ", "))
		}
		if len(rules) > 0 {
			named := make([]string, 0, abuseIPDBRulesShown)
			for i, r := range rules {
				if i == abuseIPDBRulesShown {
					break
				}
				named = append(named, r.id+" "+r.message)
			}
			b.WriteString("; rules: ")
			b.WriteString(strings.Join(named, ", "))
			if len(rules) > abuseIPDBRulesShown {
				fmt.Fprintf(&b, " and %d more", len(rules)-abuseIPDBRulesShown)
			}
		}
		if err := cw.Write([]string{
			g.ip,
			abuseIPDBWebAttack,
			g.last.UTC().Format(time.RFC3339),
			truncateRunes(b.String(), abuseIPDBCommentMax),
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

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit])
}
