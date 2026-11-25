package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/portbyte-id/server-panel/internal/metrics"
)

// AlertsService is the part of *metrics.Alerts the alert routes use. A nil
// AlertsService leaves every /api/alerts route answering 503.
type AlertsService interface {
	List(ctx context.Context) ([]metrics.RuleView, error)
	Get(ctx context.Context, id int64) (metrics.Rule, error)
	Create(ctx context.Context, r metrics.Rule) (metrics.Rule, error)
	Update(ctx context.Context, id int64, r metrics.Rule) (metrics.Rule, error)
	Delete(ctx context.Context, id int64) error
}

func (s *server) alertsRoutes(mux *http.ServeMux) {
	h := func(fn func(AlertsService, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Alerts == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"alert rules are not available; see the daemon log")
				return
			}
			fn(s.deps.Alerts, w, r)
		}))
	}
	mux.Handle("GET /api/alerts", h(alertsList))
	mux.Handle("POST /api/alerts/rules", h(alertsCreate))
	mux.Handle("PUT /api/alerts/rules/{id}", h(alertsUpdate))
	mux.Handle("DELETE /api/alerts/rules/{id}", h(alertsDelete))
}

// alertRuleBody is the JSON of a rule write. On an update an omitted field keeps
// its stored value; on a create, Enabled defaults to true.
type alertRuleBody struct {
	Kind            *string  `json:"kind"`
	Target          *string  `json:"target"`
	Threshold       *float64 `json:"threshold"`
	ClearThreshold  *float64 `json:"clear_threshold"`
	ForSeconds      *int     `json:"for_seconds"`
	ClearForSeconds *int     `json:"clear_for_seconds"`
	Severity        *string  `json:"severity"`
	Enabled         *bool    `json:"enabled"`
}

// onto applies the fields that were sent over base.
func (b alertRuleBody) onto(base metrics.Rule) metrics.Rule {
	if b.Kind != nil {
		base.Kind = *b.Kind
	}
	if b.Target != nil {
		base.Target = *b.Target
	}
	if b.Threshold != nil {
		base.Threshold = *b.Threshold
	}
	if b.ClearThreshold != nil {
		base.ClearThreshold = *b.ClearThreshold
	}
	if b.ForSeconds != nil {
		base.ForSeconds = *b.ForSeconds
	}
	if b.ClearForSeconds != nil {
		base.ClearForSeconds = *b.ClearForSeconds
	}
	if b.Severity != nil {
		base.Severity = *b.Severity
	}
	if b.Enabled != nil {
		base.Enabled = *b.Enabled
	}
	return base
}

func alertsFail(w http.ResponseWriter, err error) {
	var fe *metrics.FieldError
	switch {
	case errors.As(err, &fe):
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope{Error: errInvalidInput, Detail: fe.Detail, Field: fe.Field})
	case errors.Is(err, metrics.ErrRuleNotFound):
		writeError(w, http.StatusNotFound, errNotFound, "no such alert rule")
	case errors.Is(err, metrics.ErrBuiltinRule):
		writeError(w, http.StatusConflict, errConflict, "a built-in rule cannot be deleted; turn it off instead")
	default:
		writeError(w, http.StatusInternalServerError, errInternal, "alert request failed; see the daemon log")
	}
}

func alertRuleID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusNotFound, errNotFound, "no such alert rule")
		return 0, false
	}
	return id, true
}

func alertsList(a AlertsService, w http.ResponseWriter, r *http.Request) {
	rules, err := a.List(r.Context())
	if err != nil {
		alertsFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules, "kinds": metrics.RuleKinds()})
}

func alertsCreate(a AlertsService, w http.ResponseWriter, r *http.Request) {
	var body alertRuleBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	rule, err := a.Create(r.Context(), body.onto(metrics.Rule{Target: "*", Enabled: true, ForSeconds: 300, ClearForSeconds: 300}))
	if err != nil {
		alertsFail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func alertsUpdate(a AlertsService, w http.ResponseWriter, r *http.Request) {
	id, ok := alertRuleID(w, r)
	if !ok {
		return
	}
	var body alertRuleBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	cur, err := a.Get(r.Context(), id)
	if err != nil {
		alertsFail(w, err)
		return
	}
	rule, err := a.Update(r.Context(), id, body.onto(cur))
	if err != nil {
		alertsFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func alertsDelete(a AlertsService, w http.ResponseWriter, r *http.Request) {
	id, ok := alertRuleID(w, r)
	if !ok {
		return
	}
	if err := a.Delete(r.Context(), id); err != nil {
		alertsFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
