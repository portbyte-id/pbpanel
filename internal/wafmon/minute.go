package wafmon

import (
	"sort"
	"time"

	"github.com/portbyte-id/server-panel/internal/hist"
)

// minuteKeep bounds how long an undrained minute stays in memory. The metrics
// recorder drains every minute; this only matters when nothing does.
const minuteKeep = 2 * time.Hour

// Minute is one site's traffic for one minute, from the same parsed lines the
// live buckets are built from. It is what the metrics history stores: counts, the
// two time histograms, and nothing about individual requests.
type Minute struct {
	Domain string
	// Minute is the unix start of the minute.
	Minute              int64
	Req, S2, S3, S4, S5 int64
	Bytes               int64
	// RT is the request time and URT the upstream time, as 20-bin histograms.
	RT, URT hist.H
}

func (m *Minute) add(r Request) {
	m.Req++
	switch r.Status / 100 {
	case 2:
		m.S2++
	case 3:
		m.S3++
	case 4:
		m.S4++
	case 5:
		m.S5++
	}
	m.Bytes += r.Bytes
	if r.hasMS {
		m.RT.Add(r.MS / 1000)
	}
	if r.hasURT {
		m.URT.Add(r.URT / 1000)
	}
}

// addMinute files a request into its minute. c.mu is held. A line for a minute
// that was already handed over cannot be added to it any more: it is counted as
// late instead, so the history never double counts and the loss is visible.
func (c *Collector) addMinute(domain string, r Request) {
	minute := r.Time.Unix() / 60 * 60
	if minute <= c.drained[domain] {
		c.late++
		return
	}
	ms := c.minutes[domain]
	if ms == nil {
		ms = map[int64]*Minute{}
		c.minutes[domain] = ms
	}
	m := ms[minute]
	if m == nil {
		m = &Minute{Domain: domain, Minute: minute}
		ms[minute] = m
	}
	m.add(r)
}

// DrainMinutes returns, and forgets, every minute that ended at or before
// closedBefore, oldest first. The recorder passes now minus a grace period so a
// line that was still being written when the minute turned is counted in it.
//
// While any log has unread backlog, nothing is drained: the minutes are
// incomplete, and handing them over would lose the rest to the late counter.
func (c *Collector) DrainMinutes(closedBefore time.Time) []Minute {
	if c.backlog.Load() {
		return nil
	}
	cut := closedBefore.Unix()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Minute
	for domain, ms := range c.minutes {
		for k, m := range ms {
			if k+60 > cut {
				continue
			}
			out = append(out, *m)
			delete(ms, k)
			c.drained[domain] = max(c.drained[domain], k)
		}
		if len(ms) == 0 {
			delete(c.minutes, domain)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Minute != out[j].Minute {
			return out[i].Minute < out[j].Minute
		}
		return out[i].Domain < out[j].Domain
	})
	return out
}

// LateLines is how many access lines arrived after their minute had been handed
// to the history, and so are missing from it.
func (c *Collector) LateLines() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.late
}

// pruneMinutes drops undrained minutes older than minuteKeep and the state of
// sites that no longer exist. c.mu is held.
func (c *Collector) pruneMinutes(live map[string]bool, now time.Time) {
	horizon := now.Add(-minuteKeep).Unix()
	for domain, ms := range c.minutes {
		if !live[domain] {
			delete(c.minutes, domain)
			continue
		}
		for k := range ms {
			if k < horizon {
				delete(ms, k)
			}
		}
	}
	for domain := range c.drained {
		if !live[domain] {
			delete(c.drained, domain)
		}
	}
}
