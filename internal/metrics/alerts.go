package metrics

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/portbyte-id/server-panel/internal/notify"
)

// Alert states.
const (
	stateOK       = "ok"
	statePending  = "pending"
	stateFiring   = "firing"
	stateClearing = "clearing"
)

// Observations is the current value of every metric a rule can watch: kind, then
// target (a mount, a service, a domain, or "host"). A target absent from a round
// has no recent data; the evaluator treats that as the condition having gone away.
type Observations map[string]map[string]float64

// Observer reads the current values.
type Observer interface {
	Observe(ctx context.Context, now time.Time) (Observations, error)
}

// AlertsOptions configure Alerts.
type AlertsOptions struct {
	Rules   *RuleStore
	Observe Observer
	// Emit receives an event on each transition into firing and out of it. Nil
	// means notify.Nop.
	Emit notify.Emitter
	// Enabled reports whether evaluation is on (metrics.alerts_enabled); nil means on.
	Enabled func() bool
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Alerts evaluates the rules after each flush and owns their CRUD, so a rule that
// is deleted or switched off while firing resolves its alert first.
type Alerts struct {
	rules   *RuleStore
	observe Observer
	emit    notify.Emitter
	enabled func() bool
	now     func() time.Time
}

// NewAlerts builds the evaluator.
func NewAlerts(o AlertsOptions) (*Alerts, error) {
	if o.Rules == nil || o.Observe == nil {
		return nil, fmt.Errorf("metrics: AlertsOptions.Rules and Observe are required")
	}
	a := &Alerts{rules: o.Rules, observe: o.Observe, emit: notify.Or(o.Emit), enabled: o.Enabled, now: o.Now}
	if a.now == nil {
		a.now = time.Now
	}
	return a, nil
}

// Evaluate runs one pass. Each (rule, target) moves ok -> pending -> firing ->
// clearing -> ok: it fires when the value has stayed past the threshold for the
// rule's `for`, and clears when it has stayed past the clear threshold for
// `clear_for`. Between the two thresholds nothing changes, which is what keeps a
// value hovering at the line from flapping. Events go out on transitions only.
func (a *Alerts) Evaluate(ctx context.Context) error {
	if a.enabled != nil && !a.enabled() {
		return nil
	}
	now := a.now()
	rules, err := a.rules.List(ctx)
	if err != nil {
		return err
	}
	states, err := a.rules.States(ctx)
	if err != nil {
		return err
	}
	byKey := map[stateKey]State{}
	for _, s := range states {
		byKey[stateKey{s.RuleID, s.Target}] = s
	}
	obs, err := a.observe.Observe(ctx, now)
	if err != nil {
		return err
	}

	var firstErr error
	keep := func(e error) {
		if e != nil && firstErr == nil {
			firstErr = e
		}
	}
	seen := map[stateKey]bool{}
	for _, r := range rules {
		if !r.Enabled {
			keep(a.resolveOpen(ctx, r, byKey, nil, now))
			continue
		}
		values := obs[r.Kind]
		for target, v := range values {
			if r.Target != "*" && r.Target != target {
				continue
			}
			k := stateKey{r.ID, target}
			seen[k] = true
			keep(a.step(ctx, r, target, v, byKey[k], now))
		}
		keep(a.resolveOpen(ctx, r, byKey, seen, now))
	}
	return firstErr
}

type stateKey struct {
	rule   int64
	target string
}

func (a *Alerts) step(ctx context.Context, r Rule, target string, v float64, cur State, now time.Time) error {
	info := ruleKinds[r.Kind]
	breach := v >= r.Threshold
	cleared := v <= r.ClearThreshold
	if info.below {
		breach = v <= r.Threshold
		cleared = v >= r.ClearThreshold
	}
	if cur.State == "" {
		cur = State{RuleID: r.ID, Target: target, State: stateOK, Since: now.Unix()}
	}
	cur.Value = &v
	elapsed := now.Unix() - cur.Since

	switch cur.State {
	case stateOK:
		if !breach {
			return nil
		}
		if r.ForSeconds == 0 {
			return a.fire(ctx, r, cur, now)
		}
		cur.State, cur.Since = statePending, now.Unix()
		return a.rules.putState(ctx, cur)
	case statePending:
		switch {
		case !breach:
			return a.rules.dropState(ctx, r.ID, target)
		case elapsed >= int64(r.ForSeconds):
			return a.fire(ctx, r, cur, now)
		}
		return a.rules.putState(ctx, cur)
	case stateFiring:
		if !cleared {
			return a.rules.putState(ctx, cur)
		}
		if r.ClearForSeconds == 0 {
			return a.resolve(ctx, r, cur, now)
		}
		cur.State, cur.Since = stateClearing, now.Unix()
		return a.rules.putState(ctx, cur)
	case stateClearing:
		switch {
		case !cleared:
			// Back past the clear line without having stayed: still the same alert,
			// so no second notification.
			cur.State, cur.Since = stateFiring, now.Unix()
			return a.rules.putState(ctx, cur)
		case elapsed >= int64(r.ClearForSeconds):
			return a.resolve(ctx, r, cur, now)
		}
		return a.rules.putState(ctx, cur)
	}
	return nil
}

func (a *Alerts) fire(ctx context.Context, r Rule, cur State, now time.Time) error {
	a.emit.Emit(ctx, event(r, cur.Target, *cur.Value, false, now))
	cur.State, cur.Since = stateFiring, now.Unix()
	return a.rules.putState(ctx, cur)
}

func (a *Alerts) resolve(ctx context.Context, r Rule, cur State, now time.Time) error {
	v := 0.0
	if cur.Value != nil {
		v = *cur.Value
	}
	a.emit.Emit(ctx, event(r, cur.Target, v, true, now))
	return a.rules.dropState(ctx, r.ID, cur.Target)
}

// resolveOpen closes the alerts of a rule that no longer applies: switched off,
// or a target that has no data any more (a service removed, a certificate
// replaced by one under another name). seen is nil for a disabled rule, which
// closes every one of its targets.
func (a *Alerts) resolveOpen(ctx context.Context, r Rule, byKey map[stateKey]State, seen map[stateKey]bool, now time.Time) error {
	var firstErr error
	for k, s := range byKey {
		if k.rule != r.ID || (seen != nil && seen[k]) {
			continue
		}
		var err error
		if s.State == stateFiring || s.State == stateClearing {
			err = a.resolve(ctx, r, s, now)
		} else {
			err = a.rules.dropState(ctx, r.ID, k.target)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// event builds the notification for a transition.
func event(r Rule, target string, v float64, resolved bool, now time.Time) notify.Event {
	link := "/"
	switch r.Kind {
	case RuleSite5xxPct:
		link = "/sites/" + target + "?section=metrics"
	case RuleCertDays:
		link = "/sites/" + target + "?section=ssl"
	}
	return notify.Event{
		Kind:     notify.KindMetricsThreshold,
		Severity: notify.Severity(r.Severity),
		Key:      fmt.Sprintf("%s:%d:%s", notify.KindMetricsThreshold, r.ID, target),
		Subject:  subject(r, target, v, resolved),
		Source:   "metrics",
		At:       now,
		Resolved: resolved,
		Link:     link,
		Fields: map[string]string{
			"rule":      r.Kind,
			"target":    target,
			"value":     fmt.Sprintf("%.4g", v),
			"threshold": fmt.Sprintf("%.4g", r.Threshold),
		},
	}
}

func subject(r Rule, target string, v float64, resolved bool) string {
	switch r.Kind {
	case RuleDiskPct:
		return phrase(resolved, "Disk %s at %.0f%% (threshold %.0f%%)", "Disk %s is back to %.0f%% (threshold %.0f%%)", target, v, r.Threshold)
	case RuleMemPct:
		return phrase(resolved, "Memory at %.0f%% (threshold %.0f%%)", "Memory is back to %.0f%% (threshold %.0f%%)", v, r.Threshold)
	case RuleSwapPct:
		return phrase(resolved, "Swap at %.0f%% (threshold %.0f%%)", "Swap is back to %.0f%% (threshold %.0f%%)", v, r.Threshold)
	case RuleLoadPerCore:
		return phrase(resolved, "Load per core at %.2g (threshold %.2g)", "Load per core is back to %.2g (threshold %.2g)", v, r.Threshold)
	case RuleCPUPct:
		return phrase(resolved, "CPU at %.0f%% (threshold %.0f%%)", "CPU is back to %.0f%% (threshold %.0f%%)", v, r.Threshold)
	case RuleServiceDown:
		if resolved {
			return "Service " + target + " is running again"
		}
		return "Service " + target + " is down"
	case RuleCertDays:
		return phrase(resolved, "Certificate for %s expires in %.0f days (threshold %.0f)", "Certificate for %s has %.0f days left (threshold %.0f)", target, v, r.Threshold)
	case RuleSite5xxPct:
		return phrase(resolved, "Site %s: %.0f%% of requests fail with 5xx (threshold %.0f%%)", "Site %s: 5xx rate is back to %.0f%% (threshold %.0f%%)", target, v, r.Threshold)
	}
	return fmt.Sprintf("%s %s at %g", r.Kind, target, v)
}

func phrase(resolved bool, fire, clear string, args ...any) string {
	if resolved {
		return fmt.Sprintf(clear, args...)
	}
	return fmt.Sprintf(fire, args...)
}

// RuleView is a rule with the targets that are not quiet.
type RuleView struct {
	Rule
	States []State `json:"states"`
}

// List returns the rules with their open states.
func (a *Alerts) List(ctx context.Context) ([]RuleView, error) {
	rules, err := a.rules.List(ctx)
	if err != nil {
		return nil, err
	}
	states, err := a.rules.States(ctx)
	if err != nil {
		return nil, err
	}
	by := map[int64][]State{}
	for _, s := range states {
		by[s.RuleID] = append(by[s.RuleID], s)
	}
	out := make([]RuleView, 0, len(rules))
	for _, r := range rules {
		st := by[r.ID]
		if st == nil {
			st = []State{}
		}
		sort.Slice(st, func(i, j int) bool { return st[i].Target < st[j].Target })
		out = append(out, RuleView{Rule: r, States: st})
	}
	return out, nil
}

// Get returns one rule.
func (a *Alerts) Get(ctx context.Context, id int64) (Rule, error) { return a.rules.Get(ctx, id) }

// Create adds a rule.
func (a *Alerts) Create(ctx context.Context, r Rule) (Rule, error) { return a.rules.Create(ctx, r) }

// Update changes a rule. When it switches the rule off, or moves it to another
// target, its open alerts are resolved first, so nothing is left firing forever.
func (a *Alerts) Update(ctx context.Context, id int64, r Rule) (Rule, error) {
	cur, err := a.rules.Get(ctx, id)
	if err != nil {
		return Rule{}, err
	}
	next, err := a.rules.Update(ctx, id, r)
	if err != nil {
		return Rule{}, err
	}
	if cur.Enabled && !next.Enabled || cur.Kind != next.Kind || cur.Target != next.Target {
		if err := a.closeAll(ctx, cur); err != nil {
			return Rule{}, err
		}
	}
	return next, nil
}

// Delete removes a rule, resolving its open alerts first.
func (a *Alerts) Delete(ctx context.Context, id int64) error {
	cur, err := a.rules.Get(ctx, id)
	if err != nil {
		return err
	}
	if cur.Builtin {
		return ErrBuiltinRule
	}
	if err := a.closeAll(ctx, cur); err != nil {
		return err
	}
	return a.rules.Delete(ctx, id)
}

// closeAll resolves every open alert of a rule, using the rule as it was.
func (a *Alerts) closeAll(ctx context.Context, r Rule) error {
	states, err := a.rules.States(ctx)
	if err != nil {
		return err
	}
	now := a.now()
	for _, s := range states {
		if s.RuleID != r.ID {
			continue
		}
		if s.State == stateFiring || s.State == stateClearing {
			if err := a.resolve(ctx, r, s, now); err != nil {
				return err
			}
			continue
		}
		if err := a.rules.dropState(ctx, r.ID, s.Target); err != nil {
			return err
		}
	}
	return nil
}
