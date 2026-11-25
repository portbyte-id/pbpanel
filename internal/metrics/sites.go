package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/portbyte-id/server-panel/internal/hist"
)

const siteFifteen = 900

// SiteMinute is one site's traffic for one closed minute, as the access-log
// collector aggregated it.
type SiteMinute struct {
	Domain string
	// Minute is the unix start of the minute.
	Minute              int64
	Req, S2, S3, S4, S5 int64
	Bytes               int64
	RT, URT             hist.H
}

// SiteSource hands over the minutes that have closed. Each minute is returned
// once: the source forgets it.
type SiteSource interface {
	DrainMinutes(closedBefore time.Time) []SiteMinute
}

type siteRow struct {
	SiteMinute
	key int64
}

type siteBatch struct {
	rows []siteRow
	wms  map[string]int64 // domain -> newest minute in this batch
}

// prepareSites drains the source and drops minutes at or before a site's
// persisted watermark. The collector backfills part of each log at start, so
// after a restart it replays minutes that were already written; the watermark is
// what keeps them from being counted twice.
func (r *Recorder) prepareSites(ctx context.Context, now time.Time) (siteBatch, error) {
	b := siteBatch{wms: map[string]int64{}}
	if r.o.Sites == nil || !r.siteEnabled() {
		return b, nil
	}
	mins := r.o.Sites.DrainMinutes(now.Add(-rollupGrace))
	wm := map[string]int64{}
	for _, m := range mins {
		w, seen := wm[m.Domain]
		if !seen {
			if v, ok := r.o.DB.getMeta(ctx, "site_wm:"+m.Domain); ok {
				w, _ = strconv.ParseInt(v, 10, 64)
			}
			wm[m.Domain] = w
		}
		if m.Minute <= w {
			continue
		}
		id, err := r.o.DB.KeyID(ctx, KindSite, m.Domain)
		if err != nil {
			return siteBatch{}, err
		}
		b.rows = append(b.rows, siteRow{SiteMinute: m, key: id})
		b.wms[m.Domain] = max(b.wms[m.Domain], m.Minute)
	}
	return b, nil
}

// SetSitesEnabled turns the per-site history on or off (metrics.sites_enabled).
func (r *Recorder) SetSitesEnabled(on bool) {
	r.mu.Lock()
	r.sitesOff = !on
	r.mu.Unlock()
}

func (r *Recorder) siteEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.sitesOff
}

func writeSites(ctx context.Context, tx *sql.Tx, b siteBatch) error {
	for _, s := range b.rows {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO site_1m
			(ts, key, req, s2, s3, s4, s5, bytes, rt_hist, urt_hist) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			s.Minute, s.key, s.Req, s.S2, s.S3, s.S4, s.S5, s.Bytes, s.RT.Encode(), s.URT.Encode()); err != nil {
			return fmt.Errorf("metrics: insert site row: %w", err)
		}
	}
	for domain, w := range b.wms {
		if err := setMeta(ctx, tx, "site_wm:"+domain, strconv.FormatInt(w, 10)); err != nil {
			return fmt.Errorf("metrics: save site watermark: %w", err)
		}
	}
	return nil
}

type siteAcc struct {
	ts, key             int64
	req, s2, s3, s4, s5 int64
	bytes               int64
	rt, urt             hist.H
}

// rollupSites merges site minutes into quarter hours. Histograms are merged by
// adding bins, which is why they are fixed-width.
func rollupSites(ctx context.Context, tx *sql.Tx, upTo int64) error {
	from, ok, err := watermark(ctx, tx, "site_15m", "site_1m")
	if err != nil || !ok {
		return err
	}
	to := upTo / siteFifteen * siteFifteen
	if to <= from {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT ts, key, req, s2, s3, s4, s5, bytes, rt_hist, urt_hist
		FROM site_1m WHERE ts >= ? AND ts < ? ORDER BY ts, key`, from, to)
	if err != nil {
		return fmt.Errorf("metrics: read site minutes: %w", err)
	}
	type gk struct{ ts, key int64 }
	acc := map[gk]*siteAcc{}
	var order []gk
	for rows.Next() {
		var ts, key, req, s2, s3, s4, s5, bytes int64
		var rt, urt []byte
		if err := rows.Scan(&ts, &key, &req, &s2, &s3, &s4, &s5, &bytes, &rt, &urt); err != nil {
			_ = rows.Close()
			return fmt.Errorf("metrics: read site minutes: %w", err)
		}
		h1, err1 := hist.Decode(rt)
		h2, err2 := hist.Decode(urt)
		if err1 != nil || err2 != nil {
			continue // a damaged histogram costs its minute's percentiles, not the rollup
		}
		k := gk{ts / siteFifteen * siteFifteen, key}
		a := acc[k]
		if a == nil {
			a = &siteAcc{ts: k.ts, key: key}
			acc[k] = a
			order = append(order, k)
		}
		a.req += req
		a.s2 += s2
		a.s3 += s3
		a.s4 += s4
		a.s5 += s5
		a.bytes += bytes
		a.rt.Merge(h1)
		a.urt.Merge(h2)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("metrics: read site minutes: %w", err)
	}
	_ = rows.Close()
	for _, k := range order {
		a := acc[k]
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO site_15m
			(ts, key, req, s2, s3, s4, s5, bytes, rt_hist, urt_hist) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			a.ts, a.key, a.req, a.s2, a.s3, a.s4, a.s5, a.bytes, a.rt.Encode(), a.urt.Encode()); err != nil {
			return fmt.Errorf("metrics: write site quarter hour: %w", err)
		}
	}
	return setMeta(ctx, tx, "rolled_site_15m", strconv.FormatInt(to, 10))
}
