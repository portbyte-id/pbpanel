package sshauth

import (
	"context"
	"errors"
	"sort"
	"time"
)

// Bounds. A request cannot ask for an unbounded scan: the range is capped and
// the record count is capped, so a host whose journal grew by millions of lines
// still answers in bounded time and memory.
const (
	// DefaultRange is the window used when a request names none.
	DefaultRange = 24 * time.Hour
	// MinRange and MaxRange bound the `range` parameter. journald rotates a
	// journal by size, not age, so 30 days is still a scan of what is on disk.
	MinRange = 5 * time.Minute
	MaxRange = 30 * 24 * time.Hour

	// DefaultLimit and MaxLimit bound the number of attempts returned.
	DefaultLimit = 200
	MaxLimit     = 1000

	// MaxScan caps how many journal records one query may read before the newest
	// of them are parsed. A busy host logs plenty that is not an attempt, so this
	// is larger than MaxLimit.
	MaxScan = 20000

	// TopN is how many IPs and users the summary lists.
	TopN = 10
)

// Options configures a Monitor.
type Options struct {
	// Journal reads the records. Required.
	Journal Journal
	// Units defaults to DefaultUnits.
	Units []string
	// Now defaults to time.Now.
	Now func() time.Time
}

// Monitor answers queries over the sshd journal. It holds no state: each query
// reads the journal for its own window, so a restarted daemon has nothing to
// backfill and two queries do not see each other.
type Monitor struct {
	journal Journal
	units   []string
	now     func() time.Time
}

// New builds a Monitor. A Monitor with no Journal cannot answer; the caller
// keeps the route registered so it can say 503.
func New(o Options) (*Monitor, error) {
	if o.Journal == nil {
		return nil, errors.New("sshauth: a journal reader is required")
	}
	m := &Monitor{journal: o.Journal, units: o.Units, now: o.Now}
	if len(m.units) == 0 {
		m.units = DefaultUnits
	}
	if m.now == nil {
		m.now = time.Now
	}
	return m, nil
}

// Query selects the attempts to return.
type Query struct {
	// Range is the window ending now. Zero means DefaultRange.
	Range time.Duration
	// Outcome is OutcomeFailed, OutcomeSuccess, or "" for both.
	Outcome Outcome
	// Limit is the number of attempts returned, newest first. Zero means
	// DefaultLimit; anything above MaxLimit is cut to it.
	Limit int
}

// Report is the answer to one query: the attempts newest first, and a summary
// of the whole window (not only of the returned page).
type Report struct {
	From     time.Time    `json:"from"`
	To       time.Time    `json:"to"`
	Outcome  string       `json:"outcome"`
	Limit    int          `json:"limit"`
	Total    int          `json:"total"`
	Attempts []Attempt    `json:"attempts"`
	Summary  Summary      `json:"summary"`
}

// Summary is the aggregate above the table, shaped like wafmon's summary: a
// totals block and the two top lists the page shows.
type Summary struct {
	Failed    int      `json:"failed"`
	Succeeded int      `json:"succeeded"`
	// DistinctIPs and DistinctUsers count the distinct values in the window.
	DistinctIPs   int      `json:"distinct_ips"`
	DistinctUsers int      `json:"distinct_users"`
	TopIPs        []IPRow  `json:"top_ips"`
	TopUsers      []UserRow `json:"top_users"`
}

// IPRow is one row of the offenders list.
type IPRow struct {
	IP       string `json:"ip"`
	Failed   int    `json:"failed"`
	Succeeded int   `json:"succeeded"`
	Total    int    `json:"total"`
}

// UserRow is one row of the account list.
type UserRow struct {
	User      string `json:"user"`
	Failed    int    `json:"failed"`
	Succeeded int    `json:"succeeded"`
	Total     int    `json:"total"`
}

// Attempts runs one query. Every read error is ErrUnavailable: a host without a
// readable sshd journal is a missing feature, not a bug in the request.
func (m *Monitor) Attempts(ctx context.Context, q Query) (Report, error) {
	window := q.Range
	if window <= 0 {
		window = DefaultRange
	}
	if window < MinRange {
		window = MinRange
	}
	if window > MaxRange {
		window = MaxRange
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	now := m.now().UTC()
	from := now.Add(-window)
	raw, err := m.journal.Entries(ctx, m.units, from, now, MaxScan)
	if err != nil {
		return Report{}, err
	}

	var all []Attempt
	for _, line := range splitRecords(raw) {
		if a, ok := parseRecord(line); ok {
			all = append(all, a)
		}
	}

	// journalctl writes oldest first. Newest first is what the page shows, and
	// the summary is computed over the whole window whichever order they are in.
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.After(all[j].At) })

	rep := Report{
		From: from, To: now, Outcome: string(q.Outcome), Limit: limit,
		Attempts: []Attempt{},
		Summary:  summarise(all),
	}
	for _, a := range all {
		if q.Outcome != "" && a.Outcome != q.Outcome {
			continue
		}
		rep.Total++
		if len(rep.Attempts) < limit {
			rep.Attempts = append(rep.Attempts, a)
		}
	}
	return rep, nil
}

// Summary returns the aggregate alone, for a page that wants the summary before
// it loads a page of the table.
func (m *Monitor) Summary(ctx context.Context, window time.Duration, outcome Outcome) (Summary, error) {
	rep, err := m.Attempts(ctx, Query{Range: window, Outcome: outcome, Limit: 1})
	return rep.Summary, err
}

// summarise counts the totals and the top lists. The summary is over the whole
// window, whatever the page cut to, so a flood from one address still shows.
func summarise(all []Attempt) Summary {
	s := Summary{TopIPs: []IPRow{}, TopUsers: []UserRow{}}
	ips := map[string]*IPRow{}
	users := map[string]*UserRow{}
	for _, a := range all {
		if a.Outcome == OutcomeFailed {
			s.Failed++
		} else {
			s.Succeeded++
		}
		ip := ips[a.IP]
		if ip == nil {
			ip = &IPRow{IP: a.IP}
			ips[a.IP] = ip
		}
		u := users[a.User]
		if u == nil {
			u = &UserRow{User: a.User}
			users[a.User] = u
		}
		ip.Total++
		u.Total++
		if a.Outcome == OutcomeFailed {
			ip.Failed++
			u.Failed++
		} else {
			ip.Succeeded++
			u.Succeeded++
		}
	}
	s.DistinctIPs, s.DistinctUsers = len(ips), len(users)
	for _, r := range ips {
		s.TopIPs = append(s.TopIPs, *r)
	}
	for _, r := range users {
		s.TopUsers = append(s.TopUsers, *r)
	}
	// The list an operator acts on is the failures: a scanner shows up as many
	// failed attempts from one address, and one valid login must not outrank it.
	// Among equal failure counts the busier value ranks first, so an address with
	// one failure and fifty successes is still above one with a single probe. The
	// value itself breaks the last tie, so the order is stable across queries.
	sort.Slice(s.TopIPs, func(i, j int) bool {
		return topOrder(s.TopIPs[i].Failed, s.TopIPs[i].Total, s.TopIPs[j].Failed, s.TopIPs[j].Total, s.TopIPs[i].IP < s.TopIPs[j].IP)
	})
	sort.Slice(s.TopUsers, func(i, j int) bool {
		return topOrder(s.TopUsers[i].Failed, s.TopUsers[i].Total, s.TopUsers[j].Failed, s.TopUsers[j].Total, s.TopUsers[i].User < s.TopUsers[j].User)
	})
	if len(s.TopIPs) > TopN {
		s.TopIPs = s.TopIPs[:TopN]
	}
	if len(s.TopUsers) > TopN {
		s.TopUsers = s.TopUsers[:TopN]
	}
	return s
}

// topOrder sorts one top-list row above another: more failures first, then more
// attempts, and the caller's own tie-break (the value itself) last.
func topOrder(failedI, totalI, failedJ, totalJ int, lessIJ bool) bool {
	if failedI != failedJ {
		return failedI > failedJ
	}
	if totalI != totalJ {
		return totalI > totalJ
	}
	return lessIJ
}

// Available reports whether the host has a journal to read. A Journal that does
// not implement Prober is assumed usable.
func (m *Monitor) Available(ctx context.Context) error {
	p, ok := m.journal.(Prober)
	if !ok {
		return nil
	}
	return p.Probe(ctx)
}
