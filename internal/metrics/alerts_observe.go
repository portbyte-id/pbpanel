package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// recent is how old the newest row may be and still count as "now". A flush is
// every minute and the newest minute waits out a grace period, so three minutes
// without a row means the recorder is not writing.
const recent = 3 * time.Minute

// minSiteRequests is the traffic a minute needs before its 5xx share means
// anything: a handful of requests can be 100% errors without it being an outage.
const minSiteRequests = 50

// CertExpiry is one certificate's expiry, for the cert_days rules.
type CertExpiry struct {
	Domain   string
	NotAfter time.Time
}

// DBObserver reads the newest one-minute rows of metrics.db, the live snapshot
// for what the tables do not hold (swap size, core count), and the certificates.
type DBObserver struct {
	DB       *DB
	Recorder *Recorder
	// Certs lists the certificates; nil leaves the cert_days rules without data.
	Certs func(ctx context.Context) ([]CertExpiry, error)
}

// Observe implements Observer.
func (o DBObserver) Observe(ctx context.Context, now time.Time) (Observations, error) {
	obs := Observations{}
	since := now.Add(-recent).Unix()

	live, haveLive := o.Recorder.Latest()
	st := o.Recorder.Status()
	// A flush that is failing (a full disk is the likely cause) must not blind the
	// disk alert: the in-memory snapshot is the newer truth.
	flushFailing := !st.LastErrorAt.IsZero() && st.LastErrorAt.After(st.LastFlush)

	if err := o.observeHost(ctx, obs, since, live.Memory.SwapTotalBytes, len(live.CPUCores), haveLive); err != nil {
		return nil, err
	}

	if flushFailing && haveLive {
		for _, d := range live.Disks {
			put(obs, RuleDiskPct, d.Mount, d.UsedPercent)
		}
	} else if err := o.perKey(ctx, `SELECT key, used_pct, 0 FROM disk_1m WHERE ts >= ? ORDER BY ts`, since, KindMount,
		func(name string, v, _ sql.NullFloat64) {
			if v.Valid {
				put(obs, RuleDiskPct, name, v.Float64)
			}
		}); err != nil {
		return nil, err
	}

	if err := o.perKey(ctx, `SELECT key, up_frac, 0 FROM svc_1m WHERE ts >= ? ORDER BY ts`, since, KindService,
		func(name string, up, _ sql.NullFloat64) {
			if up.Valid {
				put(obs, RuleServiceDown, name, (1-up.Float64)*100)
			}
		}); err != nil {
		return nil, err
	}

	if err := o.perKey(ctx, `SELECT key, s5, req FROM site_1m WHERE ts >= ? ORDER BY ts`, since, KindSite,
		func(name string, s5, req sql.NullFloat64) {
			if s5.Valid && req.Valid && req.Float64 >= minSiteRequests {
				put(obs, RuleSite5xxPct, name, s5.Float64/req.Float64*100)
			}
		}); err != nil {
		return nil, err
	}

	if o.Certs != nil {
		certs, err := o.Certs(ctx)
		if err != nil {
			return nil, fmt.Errorf("metrics: observe certificates: %w", err)
		}
		for _, c := range certs {
			put(obs, RuleCertDays, c.Domain, c.NotAfter.Sub(now).Hours()/24)
		}
	}
	return obs, nil
}

func (o DBObserver) observeHost(ctx context.Context, obs Observations, since int64, swapTotal uint64, cores int, haveLive bool) error {
	var ts int64
	var cpu, load1, memUsed, memAvail, swapUsed sql.NullFloat64
	err := o.DB.SQL().QueryRowContext(ctx, `SELECT ts, cpu, load1, mem_used, mem_avail, swap_used FROM host_1m ORDER BY ts DESC LIMIT 1`).
		Scan(&ts, &cpu, &load1, &memUsed, &memAvail, &swapUsed)
	switch {
	case err == sql.ErrNoRows:
		return nil
	case err != nil:
		return fmt.Errorf("metrics: observe host: %w", err)
	case ts < since:
		return nil
	}
	if cpu.Valid {
		put(obs, RuleCPUPct, "host", cpu.Float64)
	}
	if memUsed.Valid && memAvail.Valid && memUsed.Float64+memAvail.Float64 > 0 {
		put(obs, RuleMemPct, "host", memUsed.Float64/(memUsed.Float64+memAvail.Float64)*100)
	}
	if haveLive && swapUsed.Valid && swapTotal > 0 {
		put(obs, RuleSwapPct, "host", swapUsed.Float64/float64(swapTotal)*100)
	}
	if haveLive && load1.Valid {
		put(obs, RuleLoadPerCore, "host", load1.Float64/float64(max(cores, 1)))
	}
	return nil
}

func put(obs Observations, kind, target string, v float64) {
	m := obs[kind]
	if m == nil {
		m = map[string]float64{}
		obs[kind] = m
	}
	m[target] = v
}

// perKey runs a query of (key, a, b) rows in time order, so the newest row of
// each series is the last one fn sees for it, and hands each to fn by name.
func (o DBObserver) perKey(ctx context.Context, q string, since int64, kind string, fn func(name string, a, b sql.NullFloat64)) error {
	names, err := o.DB.KeyNames(ctx, kind)
	if err != nil {
		return err
	}
	rows, err := o.DB.SQL().QueryContext(ctx, q, since)
	if err != nil {
		return fmt.Errorf("metrics: observe %s: %w", kind, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key int64
		var a, b sql.NullFloat64
		if err := rows.Scan(&key, &a, &b); err != nil {
			return fmt.Errorf("metrics: observe %s: %w", kind, err)
		}
		if name, ok := names[key]; ok {
			fn(name, a, b)
		}
	}
	return rows.Err()
}
