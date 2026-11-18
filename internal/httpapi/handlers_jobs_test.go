package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/jobs"
)

// TestJobsListReturnsRecentJobsAndValidates covers GET /api/jobs: the state and
// limit parameters, and the 400 for anything outside them. Auth and CSRF are
// covered by the protectedRoutes table in routes_test.go.
func TestJobsListReturnsRecentJobsAndValidates(t *testing.T) {
	h := newHarness(t)
	s := h.login()
	ctx := context.Background()

	done, err := h.deps.Jobs.Start(ctx, "test.quick", func(_ context.Context, out *jobs.Writer) error {
		return out.Line("hello")
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := h.deps.Jobs.Wait(ctx, done.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	type listBody struct {
		Jobs []jobs.Job `json:"jobs"`
	}

	t.Run("all returns the finished job", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/api/jobs", s.cookie, s.csrf, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
		}
		var got listBody
		h.decode(rec, &got)
		if len(got.Jobs) != 1 || got.Jobs[0].ID != done.ID || got.Jobs[0].State != jobs.StateSucceeded {
			t.Fatalf("jobs = %+v; want the one succeeded job", got.Jobs)
		}
	})

	t.Run("active excludes finished jobs and is an array, not null", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/api/jobs?state=active", s.cookie, s.csrf, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
		}
		if body := strings.TrimSpace(rec.Body.String()); body != `{"jobs":[]}` {
			t.Fatalf("body = %q; want an empty jobs array", body)
		}
	})

	for _, tc := range []struct {
		name string
		path string
	}{
		{"unknown state", "/api/jobs?state=running"},
		{"limit zero", "/api/jobs?limit=0"},
		{"limit over the maximum", "/api/jobs?limit=501"},
		{"limit not a number", "/api/jobs?limit=ten"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(http.MethodGet, tc.path, s.cookie, s.csrf, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400; body %s", rec.Code, rec.Body.String())
			}
			if env := h.errorEnvelopeOf(rec); env.Error != errInvalidInput {
				t.Fatalf("error class %q, want %q", env.Error, errInvalidInput)
			}
		})
	}
}
