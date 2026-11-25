package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"
)

// ServiceOptions configure a Service.
type ServiceOptions struct {
	DB       *DB
	Recorder *Recorder
	// Sites lists the domains the panel manages, so a site that has recorded
	// nothing yet is an empty series and not an unknown one. Nil means only sites
	// with history are known.
	Sites func(ctx context.Context) ([]string, error)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Service is the read side of the history: the queries, the status and the reset.
// The recorder is the write side.
type Service struct {
	db    *DB
	rec   *Recorder
	sites func(context.Context) ([]string, error)
	now   func() time.Time
}

// NewService builds a Service.
func NewService(o ServiceOptions) (*Service, error) {
	if o.DB == nil || o.Recorder == nil {
		return nil, fmt.Errorf("metrics: ServiceOptions.DB and Recorder are required")
	}
	s := &Service{db: o.DB, rec: o.Recorder, sites: o.Sites, now: o.Now}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

// Enabled reports whether the recorder is persisting.
func (s *Service) Enabled() bool { return s.rec.Status().Enabled }

// TierStatus is one table's size.
type TierStatus struct {
	Table  string `json:"table"`
	Rows   int64  `json:"rows"`
	Oldest int64  `json:"oldest"`
}

// StatusReport is what GET /api/metrics/status returns.
type StatusReport struct {
	Enabled     bool         `json:"enabled"`
	FileBytes   int64        `json:"file_bytes"`
	WALBytes    int64        `json:"wal_bytes"`
	Tiers       []TierStatus `json:"tiers"`
	LastFlush   int64        `json:"last_flush"`
	LastError   string       `json:"last_error"`
	LastErrorAt int64        `json:"last_error_at"`
	Dropped     int          `json:"dropped"`
}

var dataTables = []string{
	"host_10s", "host_1m", "host_5m", "disk_10s", "disk_1m", "disk_5m",
	"svc_30s", "svc_1m", "svc_5m", "site_1m", "site_15m",
}

// Status reports the file size, each tier's rows and oldest timestamp, and the
// recorder's last flush and error.
func (s *Service) Status(ctx context.Context) (StatusReport, error) {
	st := s.rec.Status()
	rep := StatusReport{Enabled: st.Enabled, Dropped: st.Dropped, LastError: st.LastError, Tiers: []TierStatus{}}
	if !st.LastFlush.IsZero() {
		rep.LastFlush = st.LastFlush.Unix()
	}
	if !st.LastErrorAt.IsZero() {
		rep.LastErrorAt = st.LastErrorAt.Unix()
	}
	if fi, err := os.Stat(s.db.Path()); err == nil {
		rep.FileBytes = fi.Size()
	}
	if fi, err := os.Stat(s.db.Path() + "-wal"); err == nil {
		rep.WALBytes = fi.Size()
	}
	for _, t := range dataTables {
		var n int64
		var oldest sql.NullInt64
		if err := s.db.SQL().QueryRowContext(ctx, `SELECT count(*), min(ts) FROM `+t).Scan(&n, &oldest); err != nil {
			return StatusReport{}, fmt.Errorf("metrics: status of %s: %w", t, err)
		}
		rep.Tiers = append(rep.Tiers, TierStatus{Table: t, Rows: n, Oldest: oldest.Int64})
	}
	return rep, nil
}

// Clear deletes the whole history. Alert rules live in panel.db and are not
// touched. The recorder keeps running and starts a fresh history.
func (s *Service) Clear(ctx context.Context) error {
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, t := range append(append([]string{}, dataTables...), "meta", "series_keys") {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+t); err != nil {
				return fmt.Errorf("metrics: clear %s: %w", t, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.db.keys.reset()
	return nil
}

// Recorder is the write side, for wiring that needs it.
func (s *Service) Recorder() *Recorder { return s.rec }
