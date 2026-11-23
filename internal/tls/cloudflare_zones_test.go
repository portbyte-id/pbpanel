package tls

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestCloudflareZonesPagesAndSorts serves 51 zones over two pages and checks the
// provider asks for active zones only, follows the pages, lowercases and sorts.
func TestCloudflareZonesPagesAndSorts(t *testing.T) {
	var statuses []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		statuses = append(statuses, r.URL.Query().Get("status"))
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			reply(w, http.StatusUnauthorized, nil)
			return
		}
		var zones []map[string]string
		if r.URL.Query().Get("page") == "1" {
			for i := range 50 {
				zones = append(zones, map[string]string{"name": fmt.Sprintf("z%02d.test", 50-i)})
			}
		} else {
			zones = append(zones, map[string]string{"name": "ALPHA.test"})
		}
		reply(w, http.StatusOK, zones)
	}))
	t.Cleanup(srv.Close)

	cf, err := NewCloudflare([]byte(`{"api_token":"`+testToken+`"}`), CloudflareOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := cf.Zones(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 51 || got[0] != "alpha.test" || got[1] != "z01.test" || got[50] != "z50.test" {
		t.Errorf("zones = %v", got)
	}
	if !reflect.DeepEqual(statuses, []string{"active", "active"}) {
		t.Errorf("status filters = %v, want active on both pages", statuses)
	}
	var _ ZoneLister = cf
}

func TestCloudflareZonesErrorCarriesNoToken(t *testing.T) {
	f, srv := newFakeCloudflare(t, nil)
	f.failZones = true
	cf, err := NewCloudflare([]byte(`{"api_token":"`+testToken+`"}`), CloudflareOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cf.Zones(context.Background())
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("err = %v, want a failure without the token", err)
	}
}
