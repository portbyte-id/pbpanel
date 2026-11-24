package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// itoa is strconv.Itoa under a short name, used only to build error details.
func itoa(n int) string { return strconv.Itoa(n) }

// auditEntryJSON is one row of the audit trail. Detail is passed through as raw
// JSON: the writer already redacted it, and re-marshalling it here would either
// double-encode it or lose the redaction.
type auditEntryJSON struct {
	ID     int64           `json:"id"`
	At     string          `json:"at"`
	Actor  string          `json:"actor"`
	Action string          `json:"action"`
	Target string          `json:"target"`
	Result string          `json:"result"`
	Detail json.RawMessage `json:"detail"`
}

// auditResponse is the GET /api/audit body.
type auditResponse struct {
	Entries []auditEntryJSON `json:"entries"`
	// Limit is the page size that was applied, so a client paging through the
	// trail knows how many it asked for.
	Limit int `json:"limit"`
	// NextBefore is the `before` value of the next, older page, or 0 on the last
	// one. It is the id of the oldest entry on this page.
	NextBefore int64 `json:"next_before"`
}

const (
	auditDefaultLimit = 100
	auditMaxLimit     = 500
)

// handleAudit returns the newest audit entries, newest first. With ?before= it
// returns the page of entries older than that id, so a client can walk back
// through the trail without the list shifting under it as new writes land.
func (s *server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit, ok := auditLimit(r)
	if !ok {
		writeError(w, http.StatusBadRequest, errBadRequest,
			"limit must be a whole number between 1 and "+itoa(auditMaxLimit))
		return
	}
	before, ok := auditBefore(r)
	if !ok {
		writeError(w, http.StatusBadRequest, errBadRequest, "before must be a positive entry id")
		return
	}

	// One extra row says whether an older page exists, without a second query.
	rows, err := s.deps.Store.DB().QueryContext(r.Context(),
		`SELECT id, at, actor, action, target, result, detail
		   FROM audit_log WHERE (? = 0 OR id < ?) ORDER BY id DESC LIMIT ?`,
		before, before, limit+1)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	defer func() { _ = rows.Close() }()

	resp := auditResponse{Entries: []auditEntryJSON{}, Limit: limit}
	for rows.Next() {
		var (
			e      auditEntryJSON
			detail string
		)
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &e.Target, &e.Result, &detail); err != nil {
			s.writeInternal(w, r, err)
			return
		}
		// A detail that is not JSON cannot be shown as an object. The row still
		// belongs in the trail, so it is wrapped as a JSON string rather than
		// dropped or allowed to break the whole page.
		if !json.Valid([]byte(detail)) {
			quoted, err := json.Marshal(detail)
			if err != nil {
				s.writeInternal(w, r, err)
				return
			}
			detail = string(quoted)
		}
		e.Detail = json.RawMessage(detail)
		resp.Entries = append(resp.Entries, e)
	}
	if err := rows.Err(); err != nil {
		s.writeInternal(w, r, err)
		return
	}
	if len(resp.Entries) > limit {
		resp.Entries = resp.Entries[:limit]
		resp.NextBefore = resp.Entries[limit-1].ID
	}
	writeJSON(w, http.StatusOK, resp)
}

// auditBefore reads the optional ?before= cursor. ok is false for a value that is
// present but not a positive id; absent means the newest page.
func auditBefore(r *http.Request) (before int64, ok bool) {
	raw := r.URL.Query().Get("before")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// auditLimit reads the optional ?limit= parameter. ok is false for a value that
// is present but not a usable number, which is a 400 rather than a silent
// fallback: a client that asked for 0 wants to know it got something else.
func auditLimit(r *http.Request) (limit int, ok bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return auditDefaultLimit, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > auditMaxLimit {
		return 0, false
	}
	return n, true
}
