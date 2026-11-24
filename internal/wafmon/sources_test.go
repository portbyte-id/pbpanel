package wafmon

import (
	"testing"
	"time"
)

// SourceCountsOver is the monitor's full list of addresses for the sources view.
// Unlike Traffic it is not cut to the top ten, so a country total can count every
// address the buckets kept.
func TestSourceCountsOverListsEveryAddress(t *testing.T) {
	f := newFixture(t)
	f.append("a.test.access.log",
		f.line("1.1.1.1", "/x", 200, f.now.Add(-5*time.Second)),
		f.line("1.1.1.1", "/x", 404, f.now.Add(-4*time.Second)),
		f.line("2.2.2.2", "/y", 500, f.now.Add(-30*time.Second)),
	)
	f.append("b.test.access.log", f.line("3.3.3.3", "/z", 200, f.now.Add(-2*time.Second)))
	f.c.Poll()

	got := f.c.SourceCountsOver(5 * time.Minute)
	if got.Requests != 4 {
		t.Fatalf("requests = %d, want 4", got.Requests)
	}
	if len(got.IPs) != 3 {
		t.Fatalf("addresses = %+v, want all three", got.IPs)
	}
	if top := got.IPs[0]; top.IP != "1.1.1.1" || top.Requests != 2 || top.Errors != 1 {
		t.Fatalf("busiest = %+v", top)
	}
}
