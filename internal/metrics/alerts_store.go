package metrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

// Rule kinds.
const (
	RuleDiskPct     = "disk_pct"
	RuleMemPct      = "mem_pct"
	RuleSwapPct     = "swap_pct"
	RuleLoadPerCore = "load_per_core"
	RuleCPUPct      = "cpu_pct"
	RuleServiceDown = "service_down"
	RuleCertDays    = "cert_days"
	RuleSite5xxPct  = "site_5xx_pct"
)

type kindInfo struct {
	// below is true for a value that counts down toward the threshold (days left):
	// it fires at or under it and clears at or over the clear threshold.
	below bool
	max   float64
	// hostWide kinds describe the host as a whole, so the target is always "host".
	hostWide bool
}

var ruleKinds = map[string]kindInfo{
	RuleDiskPct:     {max: 100},
	RuleMemPct:      {max: 100, hostWide: true},
	RuleSwapPct:     {max: 100, hostWide: true},
	RuleLoadPerCore: {max: 1000, hostWide: true},
	RuleCPUPct:      {max: 100, hostWide: true},
	RuleServiceDown: {max: 100},
	RuleCertDays:    {max: 3650, below: true},
	RuleSite5xxPct:  {max: 100},
}

// RuleKinds lists the kinds a rule may have, for the editor.
func RuleKinds() []string {
	return []string{RuleDiskPct, RuleMemPct, RuleSwapPct, RuleLoadPerCore, RuleCPUPct, RuleServiceDown, RuleCertDays, RuleSite5xxPct}
}

// Rule is one alert rule.
type Rule struct {
	ID              int64   `json:"id"`
	Kind            string  `json:"kind"`
	Target          string  `json:"target"`
	Threshold       float64 `json:"threshold"`
	ClearThreshold  float64 `json:"clear_threshold"`
	ForSeconds      int     `json:"for_seconds"`
	ClearForSeconds int     `json:"clear_for_seconds"`
	Severity        string  `json:"severity"`
	Enabled         bool    `json:"enabled"`
	Builtin         bool    `json:"builtin"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

// State is where one (rule, target) is in the ok, pending, firing, clearing cycle.
type State struct {
	RuleID int64    `json:"rule_id"`
	Target string   `json:"target"`
	State  string   `json:"state"`
	Since  int64    `json:"since"`
	Value  *float64 `json:"value"`
}

// Rule and state errors.
var (
	ErrRuleNotFound = errors.New("metrics: no such alert rule")
	ErrBuiltinRule  = errors.New("metrics: a built-in rule cannot be deleted; disable it instead")
)

// FieldError names the input that failed validation, for a 422.
type FieldError struct {
	Field  string
	Detail string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Detail }

// ValidateRule checks a rule write. A built-in rule keeps its kind and target:
// only its numbers, severity and switch may change.
func ValidateRule(r Rule) error {
	info, ok := ruleKinds[r.Kind]
	if !ok {
		return &FieldError{"kind", "must be one of " + strings.Join(RuleKinds(), ", ")}
	}
	if r.Target == "" || len(r.Target) > 253 || strings.ContainsAny(r.Target, "\r\n\x00") {
		return &FieldError{"target", "must be a mount, service or domain name, or *"}
	}
	for field, v := range map[string]float64{"threshold": r.Threshold, "clear_threshold": r.ClearThreshold} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > info.max {
			return &FieldError{field, fmt.Sprintf("must be between 0 and %g for %s", info.max, r.Kind)}
		}
	}
	switch {
	case info.below && r.ClearThreshold < r.Threshold:
		return &FieldError{"clear_threshold", "must be at least the threshold: the alert clears once the value climbs back above it"}
	case !info.below && r.ClearThreshold > r.Threshold:
		return &FieldError{"clear_threshold", "must not be above the threshold: the alert clears once the value falls back below it"}
	}
	if r.ForSeconds < 0 || r.ForSeconds > 86400 {
		return &FieldError{"for_seconds", "must be between 0 and 86400"}
	}
	if r.ClearForSeconds < 0 || r.ClearForSeconds > 86400 {
		return &FieldError{"clear_for_seconds", "must be between 0 and 86400"}
	}
	switch r.Severity {
	case "info", "warning", "critical":
	default:
		return &FieldError{"severity", "must be info, warning or critical"}
	}
	return nil
}

// normalize applies the defaults and the host-wide target rule.
func normalize(r Rule) Rule {
	r.Target = strings.TrimSpace(r.Target)
	if r.Target == "" {
		r.Target = "*"
	}
	if info, ok := ruleKinds[r.Kind]; ok && info.hostWide {
		r.Target = "host"
	}
	return r
}

// RuleStore is the rules and their state in panel.db.
type RuleStore struct {
	st  *store.Store
	now func() time.Time
}

// NewRuleStore wraps the panel database.
func NewRuleStore(st *store.Store) *RuleStore {
	return &RuleStore{st: st, now: time.Now}
}

const ruleColumns = `id, kind, target, threshold, clear_threshold, for_seconds, clear_for_seconds, severity, enabled, builtin, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanRule(s scanner) (Rule, error) {
	var r Rule
	var en, bi int
	if err := s.Scan(&r.ID, &r.Kind, &r.Target, &r.Threshold, &r.ClearThreshold, &r.ForSeconds, &r.ClearForSeconds,
		&r.Severity, &en, &bi, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Rule{}, err
	}
	r.Enabled, r.Builtin = en != 0, bi != 0
	return r, nil
}

// List returns every rule, oldest first.
func (s *RuleStore) List(ctx context.Context) ([]Rule, error) {
	rows, err := s.st.DB().QueryContext(ctx, `SELECT `+ruleColumns+` FROM metric_alert_rules ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("metrics: list alert rules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("metrics: list alert rules: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get returns one rule.
func (s *RuleStore) Get(ctx context.Context, id int64) (Rule, error) {
	r, err := scanRule(s.st.DB().QueryRowContext(ctx, `SELECT `+ruleColumns+` FROM metric_alert_rules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Rule{}, ErrRuleNotFound
	}
	if err != nil {
		return Rule{}, fmt.Errorf("metrics: read alert rule: %w", err)
	}
	return r, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Create stores a new, non-built-in rule.
func (s *RuleStore) Create(ctx context.Context, r Rule) (Rule, error) {
	r = normalize(r)
	if err := ValidateRule(r); err != nil {
		return Rule{}, err
	}
	ts := s.now().UTC().Format("2006-01-02T15:04:05.000Z")
	var id int64
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO metric_alert_rules
			(kind, target, threshold, clear_threshold, for_seconds, clear_for_seconds, severity, enabled, builtin, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,0,?,?)`,
			r.Kind, r.Target, r.Threshold, r.ClearThreshold, r.ForSeconds, r.ClearForSeconds, r.Severity, b2i(r.Enabled), ts, ts)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return Rule{}, fmt.Errorf("metrics: create alert rule: %w", err)
	}
	return s.Get(ctx, id)
}

// Update replaces a rule's settings. A built-in rule keeps its kind and target.
func (s *RuleStore) Update(ctx context.Context, id int64, r Rule) (Rule, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return Rule{}, err
	}
	if cur.Builtin {
		r.Kind, r.Target = cur.Kind, cur.Target
	}
	r = normalize(r)
	if err := ValidateRule(r); err != nil {
		return Rule{}, err
	}
	ts := s.now().UTC().Format("2006-01-02T15:04:05.000Z")
	err = s.st.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE metric_alert_rules SET kind=?, target=?, threshold=?, clear_threshold=?,
			for_seconds=?, clear_for_seconds=?, severity=?, enabled=?, updated_at=? WHERE id=?`,
			r.Kind, r.Target, r.Threshold, r.ClearThreshold, r.ForSeconds, r.ClearForSeconds, r.Severity, b2i(r.Enabled), ts, id)
		return err
	})
	if err != nil {
		return Rule{}, fmt.Errorf("metrics: update alert rule: %w", err)
	}
	return s.Get(ctx, id)
}

// Delete removes a rule and, by cascade, its state. Built-in rules stay.
func (s *RuleStore) Delete(ctx context.Context, id int64) error {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if cur.Builtin {
		return ErrBuiltinRule
	}
	return s.st.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM metric_alert_rules WHERE id = ?`, id)
		return err
	})
}

// States returns every state row that is not quiet.
func (s *RuleStore) States(ctx context.Context) ([]State, error) {
	rows, err := s.st.DB().QueryContext(ctx, `SELECT rule_id, target, state, since, value FROM metric_alert_state ORDER BY rule_id, target`)
	if err != nil {
		return nil, fmt.Errorf("metrics: list alert state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []State{}
	for rows.Next() {
		var st State
		var v sql.NullFloat64
		if err := rows.Scan(&st.RuleID, &st.Target, &st.State, &st.Since, &v); err != nil {
			return nil, fmt.Errorf("metrics: list alert state: %w", err)
		}
		if v.Valid {
			x := v.Float64
			st.Value = &x
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *RuleStore) putState(ctx context.Context, st State) error {
	var v any
	if st.Value != nil {
		v = *st.Value
	}
	return s.st.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO metric_alert_state (rule_id, target, state, since, value) VALUES (?,?,?,?,?)
			ON CONFLICT(rule_id, target) DO UPDATE SET state = excluded.state, since = excluded.since, value = excluded.value`,
			st.RuleID, st.Target, st.State, st.Since, v)
		return err
	})
}

func (s *RuleStore) dropState(ctx context.Context, ruleID int64, target string) error {
	return s.st.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM metric_alert_state WHERE rule_id = ? AND target = ?`, ruleID, target)
		return err
	})
}
