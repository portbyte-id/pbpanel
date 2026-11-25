package metrics

import (
	"context"

	"github.com/portbyte-id/server-panel/internal/sysinfo"
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
