package httpapi

import (
	"net/http"
	"strconv"
	"testing"
)

// TestAuditPagesByCursor proves ?before= walks the trail older one page at a
// time: each page holds only entries older than the cursor, next_before names
// the oldest entry shown, and the last page reports 0.
func TestAuditPagesByCursor(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	for i := range 5 {
		if rec := h.do(http.MethodPut, "/api/settings", s.cookie, s.csrf,
			map[string]any{"settings": []map[string]string{{"name": "login.max_attempts", "value": strconv.Itoa(3 + i)}}}); rec.Code != http.StatusOK {
			t.Fatalf("settings put %d: status = %d", i, rec.Code)
		}
	}

	var seen []int64
	before := ""
	for pages := 1; ; pages++ {
		if pages > 50 {
			t.Fatal("paging never reached the last page")
		}
		path := "/api/audit?limit=2"
		if before != "" {
			path += "&before=" + before
		}
		rec := h.do(http.MethodGet, path, s.cookie, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("page %d: status = %d (body %s)", pages, rec.Code, rec.Body.String())
		}
		var resp auditResponse
		h.decode(rec, &resp)
		if len(resp.Entries) > 2 {
			t.Fatalf("page %d holds %d entries, limit is 2", pages, len(resp.Entries))
		}
		for _, e := range resp.Entries {
			if len(seen) > 0 && e.ID >= seen[len(seen)-1] {
				t.Fatalf("page %d repeats or reorders: id %d after %d", pages, e.ID, seen[len(seen)-1])
			}
			seen = append(seen, e.ID)
		}
		if resp.NextBefore == 0 {
			break
		}
		if len(resp.Entries) == 0 || resp.NextBefore != resp.Entries[len(resp.Entries)-1].ID {
			t.Fatalf("page %d: next_before %d is not the oldest entry on the page", pages, resp.NextBefore)
		}
		before = strconv.FormatInt(resp.NextBefore, 10)
	}

	if len(seen) < 5 {
		t.Fatalf("walked %d entries, want at least the 5 audited writes", len(seen))
	}
}

// TestAuditBeforeIsValidated proves a ?before= that is not a positive id is a
// 400, the same as a bad limit.
func TestAuditBeforeIsValidated(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	for _, bad := range []string{"0", "-3", "abc"} {
		rec := h.do(http.MethodGet, "/api/audit?before="+bad, s.cookie, "", nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("before=%s: status = %d, want 400", bad, rec.Code)
		}
	}
}
