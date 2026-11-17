package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/portbyte-id/server-panel/internal/firewall"
)

// FirewallService is the part of firewall.Service the routes use. *firewall.Service
// implements it; tests substitute a fake.
type FirewallService interface {
	State(ctx context.Context) (firewall.State, error)
	Rules(ctx context.Context) ([]firewall.Rule, error)
	AddRule(ctx context.Context, r firewall.Rule) (firewall.Rule, error)
	UpdateRule(ctx context.Context, id int64, r firewall.Rule) (firewall.Rule, error)
	DeleteRule(ctx context.Context, id int64) error
	Blocklist(ctx context.Context) ([]firewall.BlockEntry, error)
	AddBlock(ctx context.Context, e firewall.BlockEntry) (firewall.BlockEntry, error)
	DeleteBlock(ctx context.Context, id int64) error
	Enable(ctx context.Context) (firewall.Pending, error)
	Apply(ctx context.Context) (firewall.Pending, error)
	Confirm(ctx context.Context, token string) error
	Revert(ctx context.Context) error
	Disable(ctx context.Context) error
}

// Firewall error classes.
const (
	errInvalidRule  = "invalid_rule"
	errApplyPending = "apply_pending"
	errNotEnabled   = "not_enabled"
	errNoPending    = "no_pending"
	errTokenMatch   = "token_mismatch"
	errExpired      = "expired"
)

// firewallRoutes registers the routes of development plan step 4.8. They are
// registered even when Deps.Firewall is nil, so the UI gets a 503 that says why.
func (s *server) firewallRoutes(mux *http.ServeMux) {
	h := func(fn func(FirewallService, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Firewall == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"the firewall is not available on this host; see the daemon log")
				return
			}
			fn(s.deps.Firewall, w, r)
		}))
	}
	mux.Handle("GET /api/firewall", h(s.fwState))
	mux.Handle("GET /api/firewall/rules", h(s.fwRules))
	mux.Handle("POST /api/firewall/rules", h(s.fwAddRule))
	mux.Handle("PUT /api/firewall/rules/{id}", h(s.fwUpdateRule))
	mux.Handle("DELETE /api/firewall/rules/{id}", h(s.fwDeleteRule))
	mux.Handle("GET /api/firewall/blocklist", h(s.fwBlocklist))
	mux.Handle("POST /api/firewall/blocklist", h(s.fwAddBlock))
	mux.Handle("DELETE /api/firewall/blocklist/{id}", h(s.fwDeleteBlock))
	mux.Handle("POST /api/firewall/enable", h(s.fwEnable))
	mux.Handle("POST /api/firewall/apply", h(s.fwApply))
	mux.Handle("POST /api/firewall/confirm", h(s.fwConfirm))
	mux.Handle("POST /api/firewall/revert", h(s.fwRevert))
	mux.Handle("POST /api/firewall/disable", h(s.fwDisable))
}

// fwFail maps a firewall error to its response. Anything unrecognised is an
// internal error: its text may carry iptables output the client has no use for.
func (s *server) fwFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, firewall.ErrPort), errors.Is(err, firewall.ErrPortOrd):
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errInvalidRule, Detail: err.Error(), Field: "portStart"})
	case errors.Is(err, firewall.ErrProto):
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errInvalidRule, Detail: err.Error(), Field: "protocol"})
	case errors.Is(err, firewall.ErrAddr), errors.Is(err, firewall.ErrEmptyNet):
		field := "source"
		if r.URL.Path == "/api/firewall/blocklist" {
			field = "addr"
		}
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errInvalidRule, Detail: err.Error(), Field: field})
	case errors.Is(err, firewall.ErrComment):
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errInvalidRule, Detail: err.Error(), Field: "comment"})
	case errors.Is(err, firewall.ErrNotFound):
		writeError(w, http.StatusNotFound, errNotFound, "no such firewall entry")
	case errors.Is(err, firewall.ErrDuplicate):
		writeError(w, http.StatusConflict, errConflict, "that entry already exists")
	case errors.Is(err, firewall.ErrPending):
		writeError(w, http.StatusConflict, errApplyPending, "a change is already waiting to be kept or reverted")
	case errors.Is(err, firewall.ErrNotEnabled):
		writeError(w, http.StatusConflict, errNotEnabled, "the firewall is not enabled; enable it first")
	case errors.Is(err, firewall.ErrNoPending):
		writeError(w, http.StatusConflict, errNoPending, "no change is waiting to be kept or reverted")
	case errors.Is(err, firewall.ErrToken):
		writeError(w, http.StatusConflict, errTokenMatch, "that is not the change currently waiting; reload the page")
	case errors.Is(err, firewall.ErrExpired):
		writeError(w, http.StatusGone, errExpired, "the 60 second window passed; the previous rules were restored")
	default:
		s.writeInternal(w, r, err)
	}
}

func (s *server) fwState(f FirewallService, w http.ResponseWriter, r *http.Request) {
	st, err := f.State(r.Context())
	if err != nil {
		s.fwFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *server) fwRules(f FirewallService, w http.ResponseWriter, r *http.Request) {
	rules, err := f.Rules(r.Context())
	if err != nil {
		s.fwFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (s *server) fwAddRule(f FirewallService, w http.ResponseWriter, r *http.Request) {
	var in firewall.Rule
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	out, err := f.AddRule(r.Context(), in)
	if err != nil {
		s.fwFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *server) fwUpdateRule(f FirewallService, w http.ResponseWriter, r *http.Request) {
	id, ok := fwID(w, r)
	if !ok {
		return
	}
	var in firewall.Rule
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	out, err := f.UpdateRule(r.Context(), id, in)
	if err != nil {
		s.fwFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) fwDeleteRule(f FirewallService, w http.ResponseWriter, r *http.Request) {
	id, ok := fwID(w, r)
	if !ok {
		return
	}
	if err := f.DeleteRule(r.Context(), id); err != nil {
		s.fwFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) fwBlocklist(f FirewallService, w http.ResponseWriter, r *http.Request) {
	entries, err := f.Blocklist(r.Context())
	if err != nil {
		s.fwFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *server) fwAddBlock(f FirewallService, w http.ResponseWriter, r *http.Request) {
	var in firewall.BlockEntry
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	out, err := f.AddBlock(r.Context(), in)
	if err != nil {
		s.fwFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *server) fwDeleteBlock(f FirewallService, w http.ResponseWriter, r *http.Request) {
	id, ok := fwID(w, r)
	if !ok {
		return
	}
	if err := f.DeleteBlock(r.Context(), id); err != nil {
		s.fwFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) fwEnable(f FirewallService, w http.ResponseWriter, r *http.Request) {
	if !fwEmptyBody(w, r) {
		return
	}
	p, err := f.Enable(r.Context())
	if err != nil {
		s.fwFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) fwApply(f FirewallService, w http.ResponseWriter, r *http.Request) {
	if !fwEmptyBody(w, r) {
		return
	}
	p, err := f.Apply(r.Context())
	if err != nil {
		s.fwFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) fwConfirm(f FirewallService, w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	if err := f.Confirm(r.Context(), in.Token); err != nil {
		s.fwFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) fwRevert(f FirewallService, w http.ResponseWriter, r *http.Request) {
	if !fwEmptyBody(w, r) {
		return
	}
	if err := f.Revert(r.Context()); err != nil {
		s.fwFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) fwDisable(f FirewallService, w http.ResponseWriter, r *http.Request) {
	if !fwEmptyBody(w, r) {
		return
	}
	if err := f.Disable(r.Context()); err != nil {
		s.fwFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func fwID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusNotFound, errNotFound, "no such firewall entry")
		return 0, false
	}
	return id, true
}

// fwEmptyBody accepts `{}` (what the UI sends) and an absent body, and rejects
// anything else, so a typo in a field name is an error rather than a silent no-op.
func fwEmptyBody(w http.ResponseWriter, r *http.Request) bool {
	if r.ContentLength == 0 {
		return true
	}
	var in struct{}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return false
	}
	return true
}
