package wafmon

import (
	"sort"
	"strings"
	"time"
)

const topN = 10

// Totals is the sum over a window.
type Totals struct {
	Requests  int     `json:"requests"`
	Bytes     int64   `json:"bytes"`
	S2xx      int     `json:"s2xx"`
	S3xx      int     `json:"s3xx"`
	S4xx      int     `json:"s4xx"`
	S5xx      int     `json:"s5xx"`
	WAFEvents int     `json:"waf_events"`
	AvgMS     float64 `json:"avg_ms"`
	MaxMS     float64 `json:"max_ms"`
}

// Point is one bucket of the series.
type Point struct {
	T         int64   `json:"t"`
	Requests  int     `json:"requests"`
	S2xx      int     `json:"s2xx"`
	S3xx      int     `json:"s3xx"`
	S4xx      int     `json:"s4xx"`
	S5xx      int     `json:"s5xx"`
	Bytes     int64   `json:"bytes"`
	AvgMS     float64 `json:"avg_ms"`
	WAFEvents int     `json:"waf_events"`
}

// IPRow, PathRow, StatusRow, AgentRow and SiteRow are the top lists.
type (
	IPRow struct {
		IP       string `json:"ip"`
		Requests int    `json:"requests"`
		Errors   int    `json:"errors"`
		Bytes    int64  `json:"bytes"`
	}
	PathRow struct {
		Path     string `json:"path"`
		Requests int    `json:"requests"`
		Errors   int    `json:"errors"`
	}
	StatusRow struct {
		Status   int `json:"status"`
		Requests int `json:"requests"`
	}
	AgentRow struct {
		Agent    string `json:"agent"`
		Requests int    `json:"requests"`
	}
	SiteRow struct {
		Domain    string `json:"domain"`
		Requests  int    `json:"requests"`
		Errors    int    `json:"errors"`
		WAFEvents int    `json:"waf_events"`
	}
)

// Traffic is the answer to a traffic query.
type Traffic struct {
	From        time.Time   `json:"from"`
	To          time.Time   `json:"to"`
	StepSeconds int         `json:"step_seconds"`
	RPSNow      float64     `json:"rps_now"`
	Totals      Totals      `json:"totals"`
	Series      []Point     `json:"series"`
	TopIPs      []IPRow     `json:"top_ips"`
	TopPaths    []PathRow   `json:"top_paths"`
	TopStatuses []StatusRow `json:"top_statuses"`
	TopAgents   []AgentRow  `json:"top_agents"`
	Sites       []SiteRow   `json:"sites"`
}

// Traffic summarises site (every site when empty) over the last window.
func (c *Collector) Traffic(site string, window time.Duration) Traffic {
	step := int64(Step.Seconds())
	now := c.now()
	last := now.Unix() / step
	first := last - int64(window/Step) + 1

	t := Traffic{
		From: time.Unix(first*step, 0).UTC(), To: now.UTC(), StepSeconds: int(step),
		Series: make([]Point, 0, last-first+1), TopIPs: []IPRow{}, TopPaths: []PathRow{},
		TopStatuses: []StatusRow{}, TopAgents: []AgentRow{}, Sites: []SiteRow{},
	}
	for k := first; k <= last; k++ {
		t.Series = append(t.Series, Point{T: k * step})
	}

	ips := map[string]*ipStat{}
	paths := map[string]*pathStat{}
	agents := map[string]int{}
	statuses := map[int]int{}
	var rtSum float64
	var rtN int
	var recent int
	ptN := make([]int, len(t.Series))

	c.mu.RLock()
	for domain, s := range c.sites {
		if site != "" && domain != site {
			continue
		}
		row := SiteRow{Domain: domain}
		for k, b := range s.buckets {
			if k < first || k > last {
				continue
			}
			p := &t.Series[k-first]
			p.Requests += b.req
			p.S2xx += b.s2
			p.S3xx += b.s3
			p.S4xx += b.s4
			p.S5xx += b.s5
			p.Bytes += b.bytes
			p.WAFEvents += b.waf
			// Held as a sum until the end, when it is divided by ptN.
			p.AvgMS += b.rtSum
			ptN[k-first] += b.rtN
			rtN += b.rtN
			rtSum += b.rtSum
			t.Totals.MaxMS = max(t.Totals.MaxMS, b.rtMax)
			row.Requests += b.req
			row.Errors += b.s4 + b.s5
			row.WAFEvents += b.waf
			if k >= last-1 {
				recent += b.req
			}
			for ip, v := range b.ips {
				m := ips[ip]
				if m == nil {
					m = &ipStat{}
					ips[ip] = m
				}
				m.req += v.req
				m.errs += v.errs
				m.bytes += v.bytes
			}
			for path, v := range b.paths {
				m := paths[path]
				if m == nil {
					m = &pathStat{}
					paths[path] = m
				}
				m.req += v.req
				m.errs += v.errs
			}
			for a, n := range b.agents {
				agents[a] += n
			}
			for st, n := range b.statuses {
				statuses[st] += n
			}
		}
		if row.Requests > 0 || row.WAFEvents > 0 {
			t.Sites = append(t.Sites, row)
		}
	}
	c.mu.RUnlock()

	for i := range t.Series {
		if ptN[i] > 0 {
			t.Series[i].AvgMS = round1(t.Series[i].AvgMS / float64(ptN[i]))
		} else {
			t.Series[i].AvgMS = 0
		}
	}

	for _, p := range t.Series {
		t.Totals.Requests += p.Requests
		t.Totals.Bytes += p.Bytes
		t.Totals.S2xx += p.S2xx
		t.Totals.S3xx += p.S3xx
		t.Totals.S4xx += p.S4xx
		t.Totals.S5xx += p.S5xx
		t.Totals.WAFEvents += p.WAFEvents
	}
	if rtN > 0 {
		t.Totals.AvgMS = round1(rtSum / float64(rtN))
	}
	t.Totals.MaxMS = round1(t.Totals.MaxMS)
	t.RPSNow = round1(float64(recent) / float64(step+now.Unix()%step))

	for ip, v := range ips {
		t.TopIPs = append(t.TopIPs, IPRow{ip, v.req, v.errs, v.bytes})
	}
	sort.Slice(t.TopIPs, func(i, j int) bool {
		return t.TopIPs[i].Requests > t.TopIPs[j].Requests || (t.TopIPs[i].Requests == t.TopIPs[j].Requests && t.TopIPs[i].IP < t.TopIPs[j].IP)
	})
	for p, v := range paths {
		t.TopPaths = append(t.TopPaths, PathRow{p, v.req, v.errs})
	}
	sort.Slice(t.TopPaths, func(i, j int) bool {
		return t.TopPaths[i].Requests > t.TopPaths[j].Requests || (t.TopPaths[i].Requests == t.TopPaths[j].Requests && t.TopPaths[i].Path < t.TopPaths[j].Path)
	})
	for st, n := range statuses {
		t.TopStatuses = append(t.TopStatuses, StatusRow{st, n})
	}
	sort.Slice(t.TopStatuses, func(i, j int) bool {
		return t.TopStatuses[i].Requests > t.TopStatuses[j].Requests || (t.TopStatuses[i].Requests == t.TopStatuses[j].Requests && t.TopStatuses[i].Status < t.TopStatuses[j].Status)
	})
	for a, n := range agents {
		t.TopAgents = append(t.TopAgents, AgentRow{a, n})
	}
	sort.Slice(t.TopAgents, func(i, j int) bool {
		return t.TopAgents[i].Requests > t.TopAgents[j].Requests || (t.TopAgents[i].Requests == t.TopAgents[j].Requests && t.TopAgents[i].Agent < t.TopAgents[j].Agent)
	})
	sort.Slice(t.Sites, func(i, j int) bool {
		return t.Sites[i].Requests > t.Sites[j].Requests || (t.Sites[i].Requests == t.Sites[j].Requests && t.Sites[i].Domain < t.Sites[j].Domain)
	})
	t.TopIPs, t.TopPaths = head(t.TopIPs), head(t.TopPaths)
	t.TopStatuses, t.TopAgents = head(t.TopStatuses), head(t.TopAgents)
	return t
}

func head[T any](s []T) []T {
	if len(s) > topN {
		return s[:topN]
	}
	return s
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

// RequestQuery filters the recent-requests ring.
type RequestQuery struct {
	Site string
	// After returns only requests with a larger seq.
	After uint64
	Limit int
	// Status is "2xx".."5xx" or an exact code; empty matches all.
	Status string
	IP     string
	// Q matches a substring of the path or the user agent, case-insensitively.
	Q string
}

// RequestPage is the answer to a requests query.
type RequestPage struct {
	Requests  []Request `json:"requests"`
	LatestSeq uint64    `json:"latest_seq"`
}

func statusMatches(filter string, status int) bool {
	if filter == "" {
		return true
	}
	if len(filter) == 3 && filter[1:] == "xx" {
		return int(filter[0]-'0') == status/100
	}
	return filter == itoa(status)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}

// Requests returns the newest matching requests first.
func (c *Collector) Requests(q RequestQuery) RequestPage {
	out := RequestPage{Requests: []Request{}}
	needle := strings.ToLower(q.Q)
	c.mu.RLock()
	defer c.mu.RUnlock()
	out.LatestSeq = c.reqSeq
	c.reqs.newestFirst(func(r Request) bool {
		if r.Seq <= q.After {
			return false
		}
		switch {
		case q.Site != "" && r.Site != q.Site,
			q.IP != "" && r.IP != q.IP,
			!statusMatches(q.Status, r.Status),
			needle != "" && !strings.Contains(strings.ToLower(r.Path), needle) && !strings.Contains(strings.ToLower(r.Agent), needle):
			return true
		}
		out.Requests = append(out.Requests, r)
		return len(out.Requests) < q.Limit
	})
	return out
}

// EventQuery filters the events ring.
type EventQuery struct {
	Site     string
	IP       string
	Rule     string
	Severity string
	Action   string
	Q        string
	Limit    int
	// Before returns only events with a smaller seq; zero means from the newest.
	Before uint64
}

// EventPage is the answer to an events query.
type EventPage struct {
	Events []Event `json:"events"`
	// NextBefore is the Before value of the next page, or 0 on the last one.
	NextBefore uint64 `json:"next_before"`
	// Total counts every match, whatever Before and Limit say.
	Total int `json:"total"`
}

func (q EventQuery) match(e Event) bool {
	if q.Site != "" && e.Site != q.Site || q.IP != "" && e.IP != q.IP || q.Action != "" && e.Action != q.Action {
		return false
	}
	if q.Rule != "" {
		found := false
		for _, m := range e.Matches {
			found = found || m.RuleID == q.Rule
		}
		if !found {
			return false
		}
	}
	if q.Severity != "" {
		found := false
		for _, m := range e.Matches {
			found = found || m.Severity == q.Severity
		}
		if !found {
			return false
		}
	}
	if q.Q != "" {
		n := strings.ToLower(q.Q)
		hit := strings.Contains(strings.ToLower(e.URI), n)
		for _, m := range e.Matches {
			hit = hit || strings.Contains(strings.ToLower(m.Message), n) || m.RuleID == q.Q
		}
		return hit
	}
	return true
}

// Events returns the newest matching events first.
func (c *Collector) Events(q EventQuery) EventPage {
	out := EventPage{Events: []Event{}}
	c.mu.RLock()
	defer c.mu.RUnlock()
	c.events.newestFirst(func(e Event) bool {
		if !q.match(e) {
			return true
		}
		out.Total++
		if q.Before != 0 && e.Seq >= q.Before {
			return true
		}
		if len(out.Events) < q.Limit {
			out.Events = append(out.Events, e)
		} else if out.NextBefore == 0 {
			out.NextBefore = out.Events[len(out.Events)-1].Seq
		}
		return true
	})
	return out
}

// RuleRow is one rule in the summary.
type RuleRow struct {
	RuleID   string    `json:"rule_id"`
	Message  string    `json:"message"`
	Severity string    `json:"severity"`
	Count    int       `json:"count"`
	Last     time.Time `json:"last"`
}

// Summary is the answer to an events-summary query.
type Summary struct {
	TopRules []RuleRow      `json:"top_rules"`
	BySite   []SiteCount    `json:"by_site"`
	ByAction map[string]int `json:"by_action"`
}

// SiteCount is how many events a site had.
type SiteCount struct {
	Domain string `json:"domain"`
	Count  int    `json:"count"`
}

// Summary counts the events of the last window by rule, site and action. Only
// the ring's events are counted, so a window longer than the ring holds reports
// the newest RingSize of them.
func (c *Collector) Summary(site string, window time.Duration) Summary {
	out := Summary{TopRules: []RuleRow{}, BySite: []SiteCount{}, ByAction: map[string]int{ActionBlocked: 0, ActionDetected: 0}}
	since := c.now().Add(-window)
	rules := map[string]*RuleRow{}
	sites := map[string]int{}
	c.mu.RLock()
	c.events.newestFirst(func(e Event) bool {
		if (site != "" && e.Site != site) || e.Time.Before(since) {
			return true
		}
		out.ByAction[e.Action]++
		sites[e.Site]++
		// A transaction counts once for the rule the row shows; a block with
		// only the summary rule matched counts under that rule.
		key := e.RuleID
		r := rules[key]
		if r == nil {
			r = &RuleRow{RuleID: e.RuleID, Message: e.Message, Severity: e.Severity}
			rules[key] = r
		}
		r.Count++
		if e.Time.After(r.Last) {
			r.Last = e.Time
		}
		return true
	})
	c.mu.RUnlock()
	for _, r := range rules {
		out.TopRules = append(out.TopRules, *r)
	}
	sort.Slice(out.TopRules, func(i, j int) bool {
		return out.TopRules[i].Count > out.TopRules[j].Count || (out.TopRules[i].Count == out.TopRules[j].Count && out.TopRules[i].RuleID < out.TopRules[j].RuleID)
	})
	if len(out.TopRules) > 20 {
		out.TopRules = out.TopRules[:20]
	}
	for d, n := range sites {
		out.BySite = append(out.BySite, SiteCount{d, n})
	}
	sort.Slice(out.BySite, func(i, j int) bool {
		return out.BySite[i].Count > out.BySite[j].Count || (out.BySite[i].Count == out.BySite[j].Count && out.BySite[i].Domain < out.BySite[j].Domain)
	})
	return out
}
