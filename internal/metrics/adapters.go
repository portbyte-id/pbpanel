package metrics

import (
	"context"
	"time"

	"github.com/portbyte-id/server-panel/internal/sysinfo"
	"github.com/portbyte-id/server-panel/internal/wafmon"
)

// UsageReader is what *sysinfo.Services provides.
type UsageReader interface {
	Usage(ctx context.Context) []sysinfo.ServiceUsage
}

// ServicesFrom adapts the service grid to the recorder's ServiceSource.
func ServicesFrom(u UsageReader) ServiceSource { return usageAdapter{u} }

type usageAdapter struct{ u UsageReader }

func (a usageAdapter) Usage(ctx context.Context) []ServiceSample {
	list := a.u.Usage(ctx)
	out := make([]ServiceSample, 0, len(list))
	for _, s := range list {
		out = append(out, ServiceSample{
			Name: s.Name, Kind: string(s.Kind), State: StateCode(string(s.State)),
			CPU: s.CPUPercent, CPUOK: s.CPUOK, RSS: int64(s.RSSBytes), RSSOK: s.RSSOK,
		})
	}
	return out
}

// MinuteDrainer is what *wafmon.Collector provides.
type MinuteDrainer interface {
	DrainMinutes(closedBefore time.Time) []wafmon.Minute
}

// SitesFrom adapts the access-log collector to the recorder's SiteSource.
func SitesFrom(d MinuteDrainer) SiteSource { return siteAdapter{d} }

type siteAdapter struct{ d MinuteDrainer }

func (a siteAdapter) DrainMinutes(closedBefore time.Time) []SiteMinute {
	list := a.d.DrainMinutes(closedBefore)
	out := make([]SiteMinute, 0, len(list))
	for _, m := range list {
		out = append(out, SiteMinute{
			Domain: m.Domain, Minute: m.Minute,
			Req: m.Req, S2: m.S2, S3: m.S3, S4: m.S4, S5: m.S5, Bytes: m.Bytes,
			RT: m.RT, URT: m.URT,
		})
	}
	return out
}
