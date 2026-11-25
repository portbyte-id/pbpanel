package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// family is a set of tables that share columns across tiers, so one aggregate
// select list rolls any tier into the next.
type family struct {
	name  string
	keyed bool
	tiers [3]string // raw, minute, coarse table names
	cols  string    // value columns, in order
	agg   string    // select list producing cols from a finer tier
}

var families = []family{
	{
		name: "host", tiers: [3]string{"host_10s", "host_1m", "host_5m"},
		cols: "cpu, cpu_max, load1, load1_max, load5, load15, mem_used, mem_avail, mem_avail_min, swap_used, net_rx, net_tx",
		agg: "avg(cpu), max(cpu_max), avg(load1), max(load1_max), avg(load5), avg(load15), " +
			"CAST(round(avg(mem_used)) AS INTEGER), CAST(round(avg(mem_avail)) AS INTEGER), min(mem_avail_min), " +
			"CAST(round(avg(swap_used)) AS INTEGER), avg(net_rx), avg(net_tx)",
	},
	{
		name: "disk", keyed: true, tiers: [3]string{"disk_10s", "disk_1m", "disk_5m"},
		cols: "used_pct, used_pct_max, used, rd_bps, wr_bps",
		agg:  "avg(used_pct), max(used_pct_max), CAST(round(avg(used)) AS INTEGER), avg(rd_bps), avg(wr_bps)",
	},
	{
		name: "svc", keyed: true, tiers: [3]string{"svc_30s", "svc_1m", "svc_5m"},
		cols: "state, up_frac, cpu, cpu_max, rss, rss_max",
		agg:  "max(state), avg(up_frac), avg(cpu), max(cpu_max), CAST(round(avg(rss)) AS INTEGER), max(rss_max)",
	},
}

const (
	minuteStep = 60
	coarseStep = 300
)

// rollupAll rolls every family's raw tier into minutes and minutes into five
// minutes, then the site minutes into quarter hours. upTo is the newest instant
// whose data is complete; a bucket is rolled only once it ends at or before it.
func rollupAll(ctx context.Context, tx *sql.Tx, upTo int64) error {
	for _, f := range families {
		if err := rollupTier(ctx, tx, f, 0, 1, minuteStep, upTo); err != nil {
			return err
		}
		if err := rollupTier(ctx, tx, f, 1, 2, coarseStep, upTo); err != nil {
			return err
		}
	}
	return rollupSites(ctx, tx, upTo)
}

// rollupTier aggregates tiers[src] into tiers[dst] for the buckets between this
// family's watermark and upTo. The watermark lives in meta, so each bucket is
// rolled exactly once and a gap in the data stays a gap.
func rollupTier(ctx context.Context, tx *sql.Tx, f family, src, dst int, step, upTo int64) error {
	from, ok, err := watermark(ctx, tx, f.tiers[dst], f.tiers[src])
	if err != nil || !ok {
		return err
	}
	to := upTo / step * step
	if to <= from {
		return nil
	}
	keyCol, keySel, group := "", "", "GROUP BY ts / ?"
	if f.keyed {
		keyCol, keySel, group = ", key", ", key", "GROUP BY ts / ?, key"
	}
	q := fmt.Sprintf(`INSERT OR REPLACE INTO %s (ts%s, %s)
		SELECT (ts / ?) * ?%s, %s FROM %s WHERE ts >= ? AND ts < ? %s`,
		f.tiers[dst], keyCol, f.cols, keySel, f.agg, f.tiers[src], group)
	if _, err := tx.ExecContext(ctx, q, step, step, from, to, step); err != nil {
		return fmt.Errorf("metrics: roll up %s into %s: %w", f.tiers[src], f.tiers[dst], err)
	}
	return setMeta(ctx, tx, "rolled_"+f.tiers[dst], strconv.FormatInt(to, 10))
}

// watermark is where the last rollup into dst stopped. With none recorded it is
// the oldest row of src rounded down; ok is false when src is empty.
func watermark(ctx context.Context, tx *sql.Tx, dst, src string) (int64, bool, error) {
	var v string
	err := tx.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = ?`, "rolled_"+dst).Scan(&v)
	switch {
	case err == nil:
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil {
			return 0, false, fmt.Errorf("metrics: bad watermark %q for %s", v, dst)
		}
		return n, true, nil
	case err != sql.ErrNoRows:
		return 0, false, fmt.Errorf("metrics: read watermark for %s: %w", dst, err)
	}
	var oldest sql.NullInt64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT min(ts) FROM %s`, src)).Scan(&oldest); err != nil {
		return 0, false, fmt.Errorf("metrics: read oldest row of %s: %w", src, err)
	}
	if !oldest.Valid {
		return 0, false, nil
	}
	return oldest.Int64, true, nil
}
