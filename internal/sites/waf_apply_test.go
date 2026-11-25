package sites

import (
	"context"
	"testing"
	"time"
)

// A client disconnect cancels the request. The apply must outlive it, or the
// agent's policy load is killed mid-run and the rollback is stranded.
func TestWAFApplyContextSurvivesRequestCancel(t *testing.T) {
	req, cancelReq := context.WithCancel(context.Background())
	apply, cancel := wafApplyContext(req)
	defer cancel()
	cancelReq()
	if err := apply.Err(); err != nil {
		t.Fatalf("the apply was cancelled with the request: %v", err)
	}
	deadline, ok := apply.Deadline()
	if !ok {
		t.Fatal("the apply has no deadline")
	}
	if left := time.Until(deadline); left <= 0 || left > wafApplyTimeout {
		t.Errorf("the deadline is %v away, want at most %v", left, wafApplyTimeout)
	}
}
