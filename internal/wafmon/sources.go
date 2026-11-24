package wafmon

import (
	"sort"
	"time"
)

// SourceCounts is every client address the monitor saw in a window, busiest
// first, with the total requests of the window. The total counts every request;
// the rows only the addresses the buckets kept (see keysPerBucket), so the rows
// can add up to less than the total on a busy host.
type SourceCounts struct {
	Requests int
	IPs      []IPRow
}

// SourceCountsOver counts the requests of every site over the last window. It
// keeps no more than the buckets hold, so it costs what Traffic costs.
func (c *Collector) SourceCountsOver(window time.Duration) SourceCounts {
	step := int64(Step.Seconds())
	now := c.now()
	last := now.Unix() / step
	first := last - int64(window/Step) + 1

	ips := map[string]*ipStat{}
	total := 0

	c.mu.RLock()
	for _, s := range c.sites {
		for k, b := range s.buckets {
			if k < first || k > last {
				continue
			}
			total += b.req
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
		}
	}
	c.mu.RUnlock()

	rows := make([]IPRow, 0, len(ips))
	for ip, v := range ips {
		rows = append(rows, IPRow{IP: ip, Requests: v.req, Errors: v.errs, Bytes: v.bytes})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Requests > rows[j].Requests || (rows[i].Requests == rows[j].Requests && rows[i].IP < rows[j].IP)
	})
	return SourceCounts{Requests: total, IPs: rows}
}
