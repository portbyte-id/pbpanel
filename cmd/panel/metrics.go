package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/metrics"
	"github.com/portbyte-id/server-panel/internal/notify"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/sysinfo"
	"github.com/portbyte-id/server-panel/internal/tls"
	"github.com/portbyte-id/server-panel/internal/wafmon"
)

// metricsStack is the history: metrics.db, the recorder that fills it and the
// service that reads it. A host where it cannot be built gets a nil stack, and
// its routes answer 503; the rest of the panel is unaffected.
type metricsStack struct {
	Service *metrics.Service
	Alerts  *metrics.Alerts
	db      *metrics.DB
	rec     *metrics.Recorder
}

// newMetrics opens metrics.db and wires the recorder to the sampler, the service
// grid and the access-log collector. Any of them may be nil: the history then
// simply has nothing from that source.
func newMetrics(ctx context.Context, st *store.Store, reg *settings.Registry, layout install.Layout, emit notify.Emitter, certs func(context.Context) ([]tls.Cert, error), sampler *sysinfo.Sampler, services *sysinfo.Services, waf *wafmon.Collector, siteList siteLister, logger *slog.Logger) *metricsStack {
	db, err := metrics.Open(layout.MetricsDBPath(), func(format string, args ...any) {
		logger.Warn("metrics history was reset", "detail", format, "args", args)
	})
	if err != nil {
		logger.Error("metrics history is not available", "error", err)
		return nil
	}

	var lastFlushErr time.Time
	opts := metrics.Options{
		DB:        db,
		Retention: metricsRetention(ctx, reg),
		Disabled:  !boolSetting(ctx, reg, settings.KeyMetricsEnabled, true),
		OnError: func(err error) {
			if time.Since(lastFlushErr) < time.Minute {
				return
			}
			lastFlushErr = time.Now()
			logger.Warn("metrics history could not be written", "error", err)
		},
	}
	if sampler != nil {
		opts.Snapshots = sampler
	}
	if services != nil {
		opts.Services = metrics.ServicesFrom(services)
	}
	if waf != nil {
		opts.Sites = metrics.SitesFrom(waf)
	}
	rec, err := metrics.NewRecorder(opts)
	if err != nil {
		logger.Error("metrics history is not available", "error", err)
		_ = db.Close()
		return nil
	}
	rec.SetSitesEnabled(boolSetting(ctx, reg, settings.KeyMetricsSitesEnabled, true))

	svcOpts := metrics.ServiceOptions{DB: db, Recorder: rec}
	if siteList != nil {
		svcOpts.Sites = func(ctx context.Context) ([]string, error) {
			list, err := siteList.List(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, len(list))
			for _, s := range list {
				out = append(out, s.Domain)
			}
			return out, nil
		}
	}
	svc, err := metrics.NewService(svcOpts)
	if err != nil {
		logger.Error("metrics history is not available", "error", err)
		_ = db.Close()
		return nil
	}

	alerts := newAlerts(st, reg, db, rec, emit, certs, logger)
	if alerts != nil {
		rec.SetAfterFlush(func(ctx context.Context) {
			if err := alerts.Evaluate(ctx); err != nil {
				logger.Warn("alert evaluation failed", "error", err)
			}
		})
	}

	// A change applies at once: the recorder reads nothing from the registry
	// itself, so the daemon pushes the new values in.
	reg.Subscribe(func(c settings.Change) {
		switch c.Name {
		case settings.KeyMetricsEnabled:
			rec.SetEnabled(c.New == "true")
		case settings.KeyMetricsSitesEnabled:
			rec.SetSitesEnabled(c.New == "true")
		case settings.KeyMetricsRawRetention, settings.KeyMetricsMinuteRetention, settings.KeyMetricsCoarseRetention:
			rec.SetRetention(metricsRetention(context.Background(), reg))
		}
	})
	return &metricsStack{Service: svc, Alerts: alerts, db: db, rec: rec}
}

// start runs the recorder until ctx ends. The returned function waits for its
// final flush, for at most the grace period, and then closes the database. The
// caller runs it after the HTTP listener has stopped, so a chart request that was
// still in flight when the daemon was told to stop never finds the file closed.
func (m *metricsStack) start(ctx context.Context) (wait func(grace time.Duration)) {
	if m == nil {
		return func(time.Duration) {}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.rec.Run(ctx)
	}()
	return func(grace time.Duration) {
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(grace):
		}
		_ = m.db.Close()
	}
}

func metricsRetention(ctx context.Context, reg *settings.Registry) metrics.Retention {
	def := metrics.DefaultRetention()
	return metrics.Retention{
		Raw:    durationSetting(ctx, reg, settings.KeyMetricsRawRetention, def.Raw),
		Minute: durationSetting(ctx, reg, settings.KeyMetricsMinuteRetention, def.Minute),
		Coarse: durationSetting(ctx, reg, settings.KeyMetricsCoarseRetention, def.Coarse),
	}
}

func boolSetting(ctx context.Context, reg *settings.Registry, name string, fallback bool) bool {
	v, err := reg.Bool(ctx, name)
	if err != nil {
		return fallback
	}
	return v
}

// newAlerts builds the threshold evaluator over panel.db's rules and the history.
// A failure leaves the history running without alerts.
func newAlerts(st *store.Store, reg *settings.Registry, db *metrics.DB, rec *metrics.Recorder, emit notify.Emitter, certs func(context.Context) ([]tls.Cert, error), logger *slog.Logger) *metrics.Alerts {
	obs := metrics.DBObserver{DB: db, Recorder: rec}
	if certs != nil {
		obs.Certs = func(ctx context.Context) ([]metrics.CertExpiry, error) {
			list, err := certs(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]metrics.CertExpiry, 0, len(list))
			for _, c := range list {
				out = append(out, metrics.CertExpiry{Domain: c.Domain, NotAfter: c.NotAfter})
			}
			return out, nil
		}
	}
	a, err := metrics.NewAlerts(metrics.AlertsOptions{
		Rules:   metrics.NewRuleStore(st),
		Observe: obs,
		Emit:    emit,
		Enabled: func() bool {
			return rec.Status().Enabled && boolSetting(context.Background(), reg, settings.KeyMetricsAlertsEnabled, true)
		},
	})
	if err != nil {
		logger.Error("metric alerts are not available", "error", err)
		return nil
	}
	return a
}
