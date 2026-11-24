package geoip

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture is the shape of a DB-IP country CSV: quoted fields, IPv4 and IPv6
// ranges, not in address order.
const fixture = `"198.51.100.0","198.51.100.255","US"
"1.0.0.0","1.0.0.255","AU"
"2001:db8::","2001:db8:ffff:ffff:ffff:ffff:ffff:ffff","de"
"203.0.113.0","203.0.113.127","JP"
`

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCountryLookup(t *testing.T) {
	db, err := Parse(strings.NewReader(fixture))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if db.Entries() != 4 {
		t.Fatalf("entries = %d, want 4", db.Entries())
	}
	cases := map[string]string{
		"1.0.0.0":        "AU", // first address of a range
		"1.0.0.255":      "AU", // last address of a range
		"1.0.1.0":        "",   // just past a range
		"198.51.100.7":   "US",
		"203.0.113.128":  "",   // gap between two ranges
		"2001:db8::1":    "DE", // IPv6, lower-case code in the file
		"2001:dc8::1":    "",
		"::ffff:1.0.0.7": "AU", // IPv4-mapped IPv6 is the IPv4 address
		"8.8.8.8":        "",
	}
	for ip, want := range cases {
		got, ok := db.Country(mustAddr(t, ip))
		if want == "" {
			if ok {
				t.Errorf("%s: got %q, want no country", ip, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%s: got %q (%v), want %q", ip, got, ok, want)
		}
	}
}

func TestNilDatabaseLooksUpNothing(t *testing.T) {
	var db *DB
	if _, ok := db.Country(mustAddr(t, "1.0.0.1")); ok {
		t.Fatal("a nil database found a country")
	}
}

func TestParseGzip(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(fixture)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Parse(&buf)
	if err != nil {
		t.Fatalf("Parse gzip: %v", err)
	}
	if got, _ := db.Country(mustAddr(t, "203.0.113.5")); got != "JP" {
		t.Fatalf("gzip lookup = %q, want JP", got)
	}
}

func TestParseRejects(t *testing.T) {
	for name, in := range map[string]string{
		"empty":        "",
		"bad address":  "1.0.0.0,not-an-ip,AU\n",
		"reversed":     "1.0.0.9,1.0.0.1,AU\n",
		"mixed family": "1.0.0.0,2001:db8::1,AU\n",
		"bad code":     "1.0.0.0,1.0.0.9,AUS\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(in)); err == nil {
				t.Fatal("Parse accepted a bad database")
			}
		})
	}
}

func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestStoreFallsBackToPreviousMonth(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if strings.Contains(r.URL.Path, "2026-11") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(gzipped(t, fixture))
	}))
	defer srv.Close()

	dir := t.TempDir()
	s := NewStore(dir)
	s.base = srv.URL + "/"
	s.now = func() time.Time { return time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC) }

	if s.Status().Installed {
		t.Fatal("an empty directory reports an installed database")
	}
	st, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !st.Installed || st.Month != "2026-10" || st.Entries != 4 {
		t.Fatalf("status = %+v, want October 2026 with 4 entries", st)
	}
	if len(asked) != 2 {
		t.Fatalf("asked %v, want the current month then the last", asked)
	}
	if got, ok := s.Country(mustAddr(t, "1.0.0.9")); !ok || got != "AU" {
		t.Fatalf("Country after fetch = %q, %v", got, ok)
	}

	// A new store on the same directory loads what was installed.
	again := NewStore(dir)
	if st := again.Status(); !st.Installed || st.Month != "2026-10" {
		t.Fatalf("reloaded status = %+v", st)
	}
	if n, _ := filepath.Glob(filepath.Join(dir, "dbip-country-lite-*.csv.gz")); len(n) != 1 {
		t.Fatalf("installed files = %v, want exactly one", n)
	}
}

func TestStoreKeepsNothingWhenDownloadIsDamaged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is not a country database"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	s := NewStore(dir)
	s.base = srv.URL + "/"
	s.now = func() time.Time { return time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC) }
	if _, err := s.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch accepted a damaged download")
	}
	if s.Status().Installed {
		t.Fatal("a damaged download was installed")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "dbip-*")); len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
}

func TestStoreRefusesOversizedDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxDownload+10)))
	}))
	defer srv.Close()
	dir := t.TempDir()
	s := NewStore(dir)
	s.base = srv.URL + "/"
	s.now = func() time.Time { return time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC) }
	if _, err := s.Fetch(context.Background()); err == nil {
		t.Fatal("an oversized download was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "dbip-country-lite-2026-11.csv.gz")); err == nil {
		t.Fatal("an oversized download was installed")
	}
}
