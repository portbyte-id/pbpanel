package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// sweepChunk bounds one delete so the writer is never held for long and the WAL
// does not balloon after a retention change.
const sweepChunk = 5000

type retentionClass int

const (
	classRaw retentionClass = iota
	classMinute
	classCoarse
)

var sweepTables = []struct {
	table string
	keyed bool
	class retentionClass
}{
	{"host_10s", false, classRaw}, {"disk_10s", true, classRaw}, {"svc_30s", true, classRaw},
	{"host_1m", false, classMinute}, {"disk_1m", true, classMinute}, {"svc_1m", true, classMinute}, {"site_1m", true, classMinute},
	{"host_5m", false, classCoarse}, {"disk_5m", true, classCoarse}, {"svc_5m", true, classCoarse}, {"site_15m", true, classCoarse},
}

func (r Retention) of(c retentionClass) time.Duration {
	switch c {
	case classRaw:
		return r.Raw
	case classMinute:
		return r.Minute
	}
	return r.Coarse
}

// Sweep deletes rows older than each tier's retention, in chunks of 5 000. It
// never runs VACUUM: freed pages are reused by later inserts, so the file stops
// growing instead of shrinking.
func (r *Recorder) Sweep(ctx context.Context, now time.Time, ret Retention) error {
	for _, t := range sweepTables {
		d := ret.of(t.class)
		if d <= 0 {
			continue
		}
		cutoff := now.Add(-d).Unix()
		for {
			n, err := r.deleteChunk(ctx, t.table, t.keyed, cutoff)
			if err != nil {
				return err
			}
			if n < sweepChunk {
				break
			}
		}
	}
	return nil
}

func (r *Recorder) deleteChunk(ctx context.Context, table string, keyed bool, cutoff int64) (int64, error) {
	q := fmt.Sprintf(`DELETE FROM %[1]s WHERE ts IN (SELECT ts FROM %[1]s WHERE ts < ? LIMIT %[2]d)`, table, sweepChunk)
	if keyed {
		q = fmt.Sprintf(`DELETE FROM %[1]s WHERE (ts, key) IN (SELECT ts, key FROM %[1]s WHERE ts < ? LIMIT %[2]d)`, table, sweepChunk)
	}
	var n int64
	err := r.o.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, q, cutoff)
		if err != nil {
			return fmt.Errorf("metrics: sweep %s: %w", table, err)
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}
