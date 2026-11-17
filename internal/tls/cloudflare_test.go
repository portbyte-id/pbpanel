package tls

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testToken = "cf-secret-token-0123456789"

// fakeCloudflare implements the three v4 calls the provider uses over an
// in-memory zone list and record table.
type fakeCloudflare struct {
	mu      sync.Mutex
	zones   map[string]string            // name -> id
	records map[string]map[string]string // id -> content (as the API returns it, quoted) by name
	deleted []string
	// failZones makes every call answer 403 with a body that echoes the token.
	failZones bool
	zoneLooks []string
}

func newFakeCloudflare(t *testing.T, zones map[string]string) (*fakeCloudflare, *httptest.Server) {
	t.Helper()
	f := &fakeCloudflare{zones: zones, records: map[string]map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func reply(w http.ResponseWriter, status int, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": status/100 == 2, "result": result, "errors": []any{}})
}

func (f *fakeCloudflare) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failZones {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":9109,"message":"Invalid token `+r.Header.Get("Authorization")+`"}]}`)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		reply(w, http.StatusUnauthorized, nil)
		return
	}
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/zones":
		name := r.URL.Query().Get("name")
		f.zoneLooks = append(f.zoneLooks, name)
		if id, ok := f.zones[name]; ok {
			reply(w, 200, []map[string]string{{"id": id}})
			return
		}
		reply(w, 200, []map[string]string{})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/dns_records"):
		var body struct {
			Type, Name, Content string
			TTL                 int
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Type != "TXT" || body.TTL == 0 || strings.HasSuffix(body.Name, ".") {
			reply(w, 400, nil)
			return
		}
		if f.records[body.Name] == nil {
			f.records[body.Name] = map[string]string{}
		}
		f.records[body.Name]["rec"+body.Content] = `"` + body.Content + `"`
		reply(w, 200, map[string]string{"id": "rec" + body.Content})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/dns_records"):
		name := r.URL.Query().Get("name")
		var out []map[string]string
		for id, c := range f.records[name] {
			out = append(out, map[string]string{"id": id, "content": c})
		}
		reply(w, 200, out)
	case r.Method == http.MethodDelete && strings.Contains(path, "/dns_records/"):
		id := path[strings.LastIndex(path, "/")+1:]
		for name, recs := range f.records {
			if _, ok := recs[id]; ok {
				delete(recs, id)
				f.deleted = append(f.deleted, name+"="+id)
			}
		}
		reply(w, 200, map[string]string{"id": id})
	default:
		http.NotFound(w, r)
	}
}

func newTestCloudflare(t *testing.T, srv *httptest.Server) *Cloudflare {
	t.Helper()
	cf, err := NewCloudflare([]byte(`{"api_token":"`+testToken+`"}`), CloudflareOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return cf
}

func TestCloudflarePresentAndCleanUp(t *testing.T) {
	f, srv := newFakeCloudflare(t, map[string]string{"example.com": "zone1"})
	cf := newTestCloudflare(t, srv)
	ctx := context.Background()
	fqdn := "_acme-challenge.www.example.com."

	if err := cf.Present(ctx, "www.example.com", fqdn, "value-a"); err != nil {
		t.Fatalf("Present: %v", err)
	}
	// The zone is found by walking up: www.example.com is not a zone, example.com is.
	got := strings.Join(f.zoneLooks, ",")
	if !strings.Contains(got, "_acme-challenge.www.example.com") || !strings.HasSuffix(got, "example.com") {
		t.Fatalf("zone lookups = %s", got)
	}
	if len(f.records["_acme-challenge.www.example.com"]) != 1 {
		t.Fatalf("records = %v", f.records)
	}

	// Wildcard and base name share a record name; cleanup of one keeps the other.
	if err := cf.Present(ctx, "example.com", "_acme-challenge.example.com.", "wild"); err != nil {
		t.Fatal(err)
	}
	if err := cf.Present(ctx, "example.com", "_acme-challenge.example.com.", "base"); err != nil {
		t.Fatal(err)
	}
	if err := cf.CleanUp(ctx, "example.com", "_acme-challenge.example.com.", "wild"); err != nil {
		t.Fatalf("CleanUp: %v", err)
	}
	left := f.records["_acme-challenge.example.com"]
	if len(left) != 1 || left["recbase"] == "" {
		t.Fatalf("after cleanup of wild, records = %v", left)
	}
	if len(f.deleted) != 1 {
		t.Fatalf("deleted = %v, want exactly the matching record", f.deleted)
	}
}

func TestCloudflareNoZone(t *testing.T) {
	_, srv := newFakeCloudflare(t, map[string]string{"other.org": "z"})
	cf := newTestCloudflare(t, srv)
	err := cf.Present(context.Background(), "example.com", "_acme-challenge.example.com.", "v")
	if err == nil || !strings.Contains(err.Error(), "no zone") {
		t.Fatalf("err = %v", err)
	}
}

func TestCloudflareErrorsNeverContainTheToken(t *testing.T) {
	f, srv := newFakeCloudflare(t, map[string]string{"example.com": "zone1"})
	cf := newTestCloudflare(t, srv)
	f.failZones = true
	err := cf.Present(context.Background(), "example.com", "_acme-challenge.example.com.", "v")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error leaks the token: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "Zone:DNS:Edit") {
		t.Errorf("error is not actionable: %v", err)
	}

	// A transport failure: the server is gone.
	srv.Close()
	err = cf.CleanUp(context.Background(), "example.com", "_acme-challenge.example.com.", "v")
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("transport error = %v", err)
	}
}

func TestCloudflareHonoursCancellation(t *testing.T) {
	_, srv := newFakeCloudflare(t, map[string]string{"example.com": "zone1"})
	cf := newTestCloudflare(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cf.Present(ctx, "example.com", "_acme-challenge.example.com.", "v"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestCloudflareCredentials(t *testing.T) {
	for name, creds := range map[string]string{
		"empty token":      `{"api_token":"  "}`,
		"global key shape": `{"email":"a@b.c","api_key":"` + testToken + `"}`,
		"not json":         `token=` + testToken,
		"unknown field":    `{"api_token":"` + testToken + `","extra":1}`,
	} {
		_, err := NewCloudflare([]byte(creds), CloudflareOptions{})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			continue
		}
		if strings.Contains(err.Error(), testToken) {
			t.Errorf("%s: error quotes the credentials: %v", name, err)
		}
	}
}

func TestProviderFactory(t *testing.T) {
	p, err := ProviderFactory(KindCloudflare, []byte(`{"api_token":"`+testToken+`"}`))
	if err != nil || p.Name() != KindCloudflare {
		t.Fatalf("cloudflare: %v, %v", p, err)
	}
	if _, err := ProviderFactory("route53", []byte(`{}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown kind: %v", err)
	}
}

// A provider built from stored credentials must not need, or change, the
// process environment.
func TestCloudflareDoesNotTouchTheEnvironment(t *testing.T) {
	t.Setenv("CF_DNS_API_TOKEN", "from-env")
	_, srv := newFakeCloudflare(t, map[string]string{"example.com": "zone1"})
	cf := newTestCloudflare(t, srv)
	if err := cf.Present(context.Background(), "example.com", "_acme-challenge.example.com.", "v"); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryDNS(t *testing.T) {
	m := NewMemoryDNS()
	ctx := context.Background()
	_ = m.Present(ctx, "example.com", "_acme-challenge.example.com.", "a")
	_ = m.Present(ctx, "example.com", "_acme-challenge.example.com.", "b")
	if got := m.Records("_acme-challenge.example.com."); len(got) != 2 {
		t.Fatalf("records = %v", got)
	}
	_ = m.CleanUp(ctx, "example.com", "_acme-challenge.example.com.", "a")
	if got := m.Records("_acme-challenge.example.com."); len(got) != 1 || got[0] != "b" {
		t.Fatalf("records = %v", got)
	}
}

func TestRedact(t *testing.T) {
	if got := redact("boom "+testToken+" boom", testToken); strings.Contains(got, testToken) {
		t.Fatal(got)
	}
	if got := redact("keep", ""); got != "keep" {
		t.Fatal(got)
	}
}
