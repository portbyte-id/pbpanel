package settings

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

// fakeStore is an in-memory Store. The real one is exercised in internal/store;
// here the registry's own logic is what is under test.
type fakeStore struct {
	mu     sync.Mutex
	values map[string]string
	failOn map[string]error
}

func newFakeStore() *fakeStore {
	return &fakeStore{values: make(map[string]string), failOn: make(map[string]error)}
}

func (f *fakeStore) LoadSetting(_ context.Context, name string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failOn[name]; err != nil {
		return "", false, err
	}
	v, ok := f.values[name]
	return v, ok, nil
}

func (f *fakeStore) LoadSettings(context.Context) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.values), nil
}

func (f *fakeStore) SaveSetting(_ context.Context, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failOn[name]; err != nil {
		return err
	}
	f.values[name] = value
	return nil
}

func newTestRegistry(t *testing.T) (*Registry, *fakeStore) {
	t.Helper()
	fs := newFakeStore()
	r, err := New(fs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, fs
}

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// The registry refuses to exist unless every key it ships has a default its own
// validator accepts. This is what keeps a bad key table from reaching a boot.
func TestEveryRegisteredKeyHasAValidDefault(t *testing.T) {
	keys := defaultKeys()
	if len(keys) == 0 {
		t.Fatal("defaultKeys returned no keys")
	}

	for _, k := range keys {
		t.Run(k.meta.Name, func(t *testing.T) {
			if k.meta.Default == "" && !k.meta.Optional {
				t.Fatalf("%s has no default", k.meta.Name)
			}
			if err := validate(k, k.meta.Default); err != nil {
				t.Fatalf("%s default %q is rejected by its own validator: %v", k.meta.Name, k.meta.Default, err)
			}
			if k.meta.Description == "" {
				t.Fatalf("%s has no description for the UI", k.meta.Name)
			}
			if k.meta.Group == "" {
				t.Fatalf("%s has no group", k.meta.Name)
			}
		})
	}
}

// New must reject the same broken table, because the daemon calls New and
// nothing else.
func TestNewRejectsAnInvalidDefault(t *testing.T) {
	bad := []key{
		{meta: Meta{Name: "x.port", Type: TypePort, Default: "70000", Description: "d", Group: "g"}},
		{meta: Meta{Name: "x.enum", Type: TypeEnum, Default: "c", Allowed: []string{"a", "b"}, Description: "d", Group: "g"}},
		{meta: Meta{Name: "x.none", Type: TypeInt, Description: "d", Group: "g"}},
		{meta: Meta{Name: "x.nodesc", Type: TypeInt, Default: "1", Group: "g"}},
		{meta: Meta{Name: "x.nogroup", Type: TypeInt, Default: "1", Description: "d"}},
		{meta: Meta{Name: "x.badcheck", Type: TypeInt, Default: "5", Description: "d", Group: "g"}, check: rangeInt(1, 4)},
	}
	for _, k := range bad {
		t.Run(k.meta.Name, func(t *testing.T) {
			if _, err := newRegistry(newFakeStore(), []key{k}); err == nil {
				t.Fatalf("newRegistry accepted key %+v", k.meta)
			}
		})
	}
}

func TestNewRejectsDuplicateKeys(t *testing.T) {
	k := key{meta: Meta{Name: "dup", Type: TypeInt, Default: "1", Description: "d", Group: "g"}}
	if _, err := newRegistry(newFakeStore(), []key{k, k}); err == nil {
		t.Fatal("newRegistry accepted the same key twice")
	}
}

func TestNewRejectsNilStore(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) succeeded")
	}
}

// Rejection per type plus each shipped key's own validator. The message must
// come from the validator, because the UI shows it verbatim.
func TestInvalidValuesAreRejected(t *testing.T) {
	r, fs := newTestRegistry(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		value   string
		wantMsg string
	}{
		{"panel.port", "", "port number"},
		{"panel.port", "0", "port number"},
		{"panel.port", "65536", "port number"},
		{"panel.port", "-1", "port number"},
		{"panel.port", "84a3", "port number"},
		{"panel.port", "080", "port number"},
		{"panel.port", "8443 ", "port number"},
		{"panel.bind", "", "must not be empty"},
		{"panel.bind", "localhost", "IP address"},
		{"panel.bind", "10.0.0.256", "IP address"},
		{"session.idle_timeout", "", "duration"},
		{"session.idle_timeout", "30", "duration"},
		{"session.idle_timeout", "30 minutes", "duration"},
		{"session.idle_timeout", "-5m", "greater than zero"},
		{"session.idle_timeout", "0s", "greater than zero"},
		{"login.max_attempts", "", "whole number"},
		{"login.max_attempts", "five", "whole number"},
		{"login.max_attempts", "0", "between 1 and 1000"},
		{"login.max_attempts", "1001", "between 1 and 1000"},
		{"login.max_attempts", "5.0", "whole number"},
		{"login.lockout", "15", "duration"},
		{"login.lockout", "-1m", "greater than zero"},
	}

	for _, c := range cases {
		t.Run(c.name+"="+c.value, func(t *testing.T) {
			err := r.Set(ctx, c.name, c.value)
			if err == nil {
				t.Fatalf("Set(%s, %q) succeeded", c.name, c.value)
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Fatalf("error = %q, want it to mention %q", err, c.wantMsg)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error is %T, want *ValidationError", err)
			}
			if ve.Reason != err.Error() {
				t.Fatalf("ValidationError.Reason = %q, Error() = %q: the UI shows Reason", ve.Reason, err)
			}
			if _, stored := fs.values[c.name]; stored {
				t.Fatalf("a rejected write stored a value for %s", c.name)
			}
		})
	}
}

func TestTLSKeysValidate(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()

	if s, err := r.Get(ctx, KeyTLSACMEDirectory); err != nil || s.Value != DefaultACMEDirectory || !strings.Contains(s.Value, "staging") {
		t.Fatalf("directory default = %+v, %v; want Let's Encrypt staging", s, err)
	}
	if n, err := r.Int(ctx, KeyTLSRenewDaysBefore); err != nil || n != 30 {
		t.Fatalf("renew days = %d, %v; want 30", n, err)
	}

	// The directory is a fixed list of CAs, not free text.
	for _, v := range []string{"http://acme.example/dir", "https://acme.example/dir", "acme.example", ""} {
		if err := r.Set(ctx, KeyTLSACMEDirectory, v); err == nil {
			t.Errorf("directory %q accepted", v)
		}
	}
	for _, v := range []string{DefaultACMEDirectory, ACMEDirectoryLetsEncrypt, ACMEDirectoryGoogle, ACMEDirectoryGoogleStaging} {
		if err := r.Set(ctx, KeyTLSACMEDirectory, v); err != nil {
			t.Errorf("directory %q rejected: %v", v, err)
		}
		if _, ok := acmeDirectoryLabels[v]; !ok {
			t.Errorf("directory %q has no label", v)
		}
	}
	// EAB credentials may be empty (Let's Encrypt) and are set for Google.
	for _, k := range []string{KeyTLSACMEEABKid, KeyTLSACMEEABHMACKey} {
		if err := r.Set(ctx, k, "abc"); err != nil {
			t.Errorf("%s rejected: %v", k, err)
		}
		if err := r.Set(ctx, k, ""); err != nil {
			t.Errorf("%s cannot be cleared: %v", k, err)
		}
	}
	for _, v := range []string{"0", "61", "x", ""} {
		if err := r.Set(ctx, KeyTLSRenewDaysBefore, v); err == nil {
			t.Errorf("renew days %q accepted", v)
		}
	}
	if err := r.Set(ctx, KeyTLSRenewDaysBefore, "60"); err != nil {
		t.Errorf("60 rejected: %v", err)
	}
}

// Each type's rules, independent of the five shipped keys, so a future key of
// any type inherits the same guarantees.
func TestTypeValidation(t *testing.T) {
	cases := []struct {
		meta  Meta
		value string
		ok    bool
	}{
		{Meta{Type: TypeInt}, "42", true},
		{Meta{Type: TypeInt}, "-7", true},
		{Meta{Type: TypeInt}, "0", true},
		{Meta{Type: TypeInt}, "007", false},
		{Meta{Type: TypeInt}, "+7", false},
		{Meta{Type: TypeInt}, "1.5", false},
		{Meta{Type: TypeInt}, "", false},

		{Meta{Type: TypeString}, "anything", true},
		{Meta{Type: TypeString}, "", false},

		{Meta{Type: TypeBool}, "true", true},
		{Meta{Type: TypeBool}, "false", true},
		{Meta{Type: TypeBool}, "TRUE", false},
		{Meta{Type: TypeBool}, "1", false},
		{Meta{Type: TypeBool}, "yes", false},

		{Meta{Type: TypeDuration}, "30m", true},
		{Meta{Type: TypeDuration}, "1h30m", true},
		{Meta{Type: TypeDuration}, "0s", true},
		{Meta{Type: TypeDuration}, "30", false},
		{Meta{Type: TypeDuration}, "m", false},

		{Meta{Type: TypePort}, "1", true},
		{Meta{Type: TypePort}, "443", true},
		{Meta{Type: TypePort}, "65535", true},
		{Meta{Type: TypePort}, "0", false},
		{Meta{Type: TypePort}, "65536", false},

		{Meta{Type: TypePath}, "/www/panel", true},
		{Meta{Type: TypePath}, "/", true},
		{Meta{Type: TypePath}, "www/panel", false},
		{Meta{Type: TypePath}, "/www/panel/", false},
		{Meta{Type: TypePath}, "/www//panel", false},
		{Meta{Type: TypePath}, "/www/../etc", false},
		{Meta{Type: TypePath}, "", false},

		{Meta{Type: TypeEnum, Allowed: []string{"a", "b"}}, "a", true},
		{Meta{Type: TypeEnum, Allowed: []string{"a", "b"}}, "b", true},
		{Meta{Type: TypeEnum, Allowed: []string{"a", "b"}}, "c", false},
		{Meta{Type: TypeEnum, Allowed: []string{"a", "b"}}, "A", false},
		{Meta{Type: TypeEnum, Allowed: []string{"a", "b"}}, "", false},
		{Meta{Type: TypeEnum}, "a", false},
	}

	for _, c := range cases {
		t.Run(string(c.meta.Type)+"="+c.value, func(t *testing.T) {
			err := validateType(c.meta, c.value)
			if c.ok && err != nil {
				t.Fatalf("validateType(%s, %q) = %v, want accepted", c.meta.Type, c.value, err)
			}
			if !c.ok && err == nil {
				t.Fatalf("validateType(%s, %q) accepted an invalid value", c.meta.Type, c.value)
			}
		})
	}
}

func TestGetFallsBackToDefault(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()

	got, err := r.Get(ctx, "panel.port")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Value != "8443" || !got.IsDefault {
		t.Fatalf("Get = %+v, want value 8443 with IsDefault true", got)
	}
	if !got.RestartRequired {
		t.Error("panel.port must be marked restart-required")
	}
}

func TestSetThenGetReturnsStoredValue(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()

	if err := r.Set(ctx, "panel.port", "9443"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := r.Get(ctx, "panel.port")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Value != "9443" || got.IsDefault {
		t.Fatalf("Get = %+v, want value 9443 with IsDefault false", got)
	}

	n, err := r.Int(ctx, "panel.port")
	if err != nil || n != 9443 {
		t.Fatalf("Int = %d, %v; want 9443", n, err)
	}
}

// The stored value is the only source: a row that no longer validates is an
// error, not a silent fallback. An operator who tightened a validator has to
// learn that the old value is gone.
func TestGetReportsAStoredValueThatNoLongerValidates(t *testing.T) {
	r, fs := newTestRegistry(t)
	fs.values["panel.port"] = "99999"

	if _, err := r.Get(context.Background(), "panel.port"); err == nil {
		t.Fatal("Get accepted a stored value outside the valid range")
	}
}

// A corrupt row must stay repairable: Set reads the old value without
// validating it.
func TestSetRepairsACorruptStoredValue(t *testing.T) {
	r, fs := newTestRegistry(t)
	fs.values["panel.port"] = "99999"

	if err := r.Set(context.Background(), "panel.port", "8443"); err != nil {
		t.Fatalf("Set could not repair a corrupt value: %v", err)
	}
	if got := fs.values["panel.port"]; got != "8443" {
		t.Fatalf("stored = %q, want 8443", got)
	}
}

func TestUnknownKeyReturnsTypedNotFound(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()

	_, err := r.Get(ctx, "no.such.key")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
	var nf *NotFoundError
	if !errors.As(err, &nf) || nf.Name != "no.such.key" {
		t.Fatalf("Get error = %#v, want *NotFoundError naming the key", err)
	}

	if err := r.Set(ctx, "no.such.key", "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Set error = %v, want ErrNotFound", err)
	}
	if _, err := r.Int(ctx, "no.such.key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Int error = %v, want ErrNotFound", err)
	}
}

func TestSubscriberNotifiedOncePerSuccessfulWrite(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()

	var got []Change
	r.Subscribe(func(c Change) { got = append(got, c) })

	if err := r.Set(ctx, "panel.port", "9443"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("subscriber ran %d times, want 1", len(got))
	}
	if got[0].Name != "panel.port" || got[0].Old != "8443" || got[0].New != "9443" {
		t.Fatalf("change = %+v, want panel.port 8443 -> 9443", got[0])
	}

	if err := r.Set(ctx, "panel.port", "10443"); err != nil {
		t.Fatalf("second Set: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("subscriber ran %d times after two writes, want 2", len(got))
	}
	if got[1].Old != "9443" || got[1].New != "10443" {
		t.Fatalf("second change = %+v, want 9443 -> 10443", got[1])
	}
}

func TestSubscriberNotNotifiedOnRejectedWrite(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()

	var got []Change
	r.Subscribe(func(c Change) { got = append(got, c) })

	if err := r.Set(ctx, "panel.port", "0"); err == nil {
		t.Fatal("Set accepted port 0")
	}
	if err := r.Set(ctx, "no.such.key", "1"); err == nil {
		t.Fatal("Set accepted an unknown key")
	}
	if len(got) != 0 {
		t.Fatalf("subscriber ran %d times on rejected writes, want 0", len(got))
	}
}

// A store failure is a failed write: no subscriber may act on a value that was
// never persisted.
func TestSubscriberNotNotifiedWhenTheStoreFails(t *testing.T) {
	r, fs := newTestRegistry(t)
	ctx := context.Background()

	var got []Change
	r.Subscribe(func(c Change) { got = append(got, c) })

	sentinel := errors.New("disk full")
	fs.failOn["panel.port"] = sentinel

	err := r.Set(ctx, "panel.port", "9443")
	if !errors.Is(err, sentinel) {
		t.Fatalf("Set error = %v, want the store's error", err)
	}
	if len(got) != 0 {
		t.Fatalf("subscriber ran %d times after a failed write, want 0", len(got))
	}
}

func TestUnsubscribeStopsNotifications(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()

	var a, b int
	unsubscribeA := r.Subscribe(func(Change) { a++ })
	r.Subscribe(func(Change) { b++ })

	if err := r.Set(ctx, "panel.port", "9443"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	unsubscribeA()
	unsubscribeA() // idempotent
	if err := r.Set(ctx, "panel.port", "10443"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if a != 1 {
		t.Fatalf("unsubscribed subscriber ran %d times, want 1", a)
	}
	if b != 2 {
		t.Fatalf("remaining subscriber ran %d times, want 2", b)
	}
}

// A callback that subscribes must not deadlock: notify copies the slice before
// calling out.
func TestSubscriberMaySubscribeDuringNotification(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()

	var inner int
	r.Subscribe(func(Change) {
		r.Subscribe(func(Change) { inner++ })
	})

	if err := r.Set(ctx, "panel.port", "9443"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if inner != 0 {
		t.Fatalf("a subscriber added mid-notification was called %d times in that write, want 0", inner)
	}
	if err := r.Set(ctx, "panel.port", "10443"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if inner != 1 {
		t.Fatalf("later subscriber ran %d times on the next write, want 1", inner)
	}
}

func TestGroupsAreOrderedAndComplete(t *testing.T) {
	r, _ := newTestRegistry(t)

	groups := r.Groups()
	wantGroups := []string{"Panel", "Login", "Session", "PHP", "Files", "TLS", "Cron", "Logs"}
	if len(groups) != len(wantGroups) {
		t.Fatalf("got %d groups, want %d: %+v", len(groups), len(wantGroups), groups)
	}
	for i, name := range wantGroups {
		if groups[i].Name != name {
			t.Fatalf("group %d = %q, want %q", i, groups[i].Name, name)
		}
	}

	wantKeys := map[string][]string{
		"Panel":   {"panel.port", "panel.bind"},
		"Login":   {"login.max_attempts", "login.lockout"},
		"Session": {"session.idle_timeout"},
		"PHP":     {"php.versions"},
		"Files": {"fm.editor.max_bytes", "fm.upload.chunk_max_bytes", "fm.upload.sweep_age",
			"fm.trash.max_bytes", "fm.trash.retention_days", "fm.archive.max_bytes", "fm.archive.max_entries"},
		"TLS": {"tls.acme.directory", "tls.acme.eab_kid", "tls.acme.eab_hmac_key",
			"tls.renew.days_before", "tls.panel.auto_ip"},
		"Cron": {"cron.allow_shell"},
		"Logs": {"logs.rotate.max_bytes", "logs.rotate.keep"},
	}
	seen := 0
	for _, g := range groups {
		names := make([]string, 0, len(g.Keys))
		for _, k := range g.Keys {
			names = append(names, k.Name)
			seen++
		}
		if strings.Join(names, ",") != strings.Join(wantKeys[g.Name], ",") {
			t.Fatalf("group %s keys = %v, want %v", g.Name, names, wantKeys[g.Name])
		}
	}
	if seen != len(defaultKeys()) {
		t.Fatalf("groups expose %d keys, want %d", seen, len(defaultKeys()))
	}
}

// Metadata is a copy: a caller editing Allowed must not change the registry.
func TestGroupsReturnsACopy(t *testing.T) {
	r, err := newRegistry(newFakeStore(), []key{{
		meta: Meta{Name: "e", Type: TypeEnum, Default: "a", Allowed: []string{"a", "b"}, Description: "d", Group: "g"},
	}})
	if err != nil {
		t.Fatalf("newRegistry: %v", err)
	}

	groups := r.Groups()
	groups[0].Keys[0].Allowed[0] = "mutated"

	if got := r.Groups()[0].Keys[0].Allowed[0]; got != "a" {
		t.Fatalf("Allowed[0] = %q after mutating a returned copy, want a", got)
	}
}

// Catalog is the store-free path `panel set -h` uses, so it must describe the
// same keys the registry does rather than a second, drifting list.
func TestCatalogMatchesRegistryGroups(t *testing.T) {
	r, _ := newTestRegistry(t)

	catalog, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	groups := r.Groups()

	if len(catalog) != len(groups) {
		t.Fatalf("Catalog has %d groups, want %d", len(catalog), len(groups))
	}
	for i := range groups {
		if catalog[i].Name != groups[i].Name {
			t.Fatalf("group %d = %q, want %q", i, catalog[i].Name, groups[i].Name)
		}
		if len(catalog[i].Keys) != len(groups[i].Keys) {
			t.Fatalf("group %s has %d keys, want %d", catalog[i].Name, len(catalog[i].Keys), len(groups[i].Keys))
		}
		for j := range groups[i].Keys {
			if !reflect.DeepEqual(catalog[i].Keys[j], groups[i].Keys[j]) {
				t.Fatalf("key %s = %+v, want %+v", groups[i].Keys[j].Name, catalog[i].Keys[j], groups[i].Keys[j])
			}
		}
	}
}

// Catalog must carry the restart flag through, since that flag is what decides
// whether the CLI tells the operator to restart the panel.
func TestCatalogCarriesRestartRequired(t *testing.T) {
	catalog, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}

	got := make(map[string]bool)
	for _, g := range catalog {
		for _, k := range g.Keys {
			got[k.Name] = k.RestartRequired
		}
	}
	for _, name := range []string{"panel.port", "panel.bind"} {
		if !got[name] {
			t.Errorf("%s is not marked restart-required", name)
		}
	}
	for _, name := range []string{"login.max_attempts", "login.lockout", "session.idle_timeout"} {
		if got[name] {
			t.Errorf("%s is marked restart-required, want live", name)
		}
	}
}

func TestSnapshotReturnsEveryKeyInOrder(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()

	if err := r.Set(ctx, "login.max_attempts", "10"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	snap, err := r.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != len(defaultKeys()) {
		t.Fatalf("Snapshot has %d entries, want %d", len(snap), len(defaultKeys()))
	}
	for i, k := range defaultKeys() {
		if snap[i].Name != k.meta.Name {
			t.Fatalf("Snapshot[%d] = %s, want %s", i, snap[i].Name, k.meta.Name)
		}
	}

	byName := make(map[string]Setting, len(snap))
	for _, s := range snap {
		byName[s.Name] = s
	}
	if s := byName["login.max_attempts"]; s.Value != "10" || s.IsDefault {
		t.Fatalf("login.max_attempts = %+v, want 10 and not default", s)
	}
	if s := byName["panel.port"]; s.Value != "8443" || !s.IsDefault {
		t.Fatalf("panel.port = %+v, want 8443 and default", s)
	}
}

func TestTypedAccessors(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()

	if n, err := r.Int(ctx, "login.max_attempts"); err != nil || n != 5 {
		t.Fatalf("Int = %d, %v; want 5", n, err)
	}
	if d, err := r.Duration(ctx, "session.idle_timeout"); err != nil || d != 30*time.Minute {
		t.Fatalf("Duration = %v, %v; want 30m", d, err)
	}
	if s, err := r.Str(ctx, "panel.bind"); err != nil || s != "0.0.0.0" {
		t.Fatalf("Str = %q, %v; want 0.0.0.0", s, err)
	}

	if _, err := r.Bool(ctx, "panel.port"); err == nil {
		t.Fatal("Bool accepted a port setting")
	}
	if _, err := r.Duration(ctx, "panel.bind"); err == nil {
		t.Fatal("Duration accepted a string setting")
	}
	if _, err := r.Int(ctx, "session.idle_timeout"); err == nil {
		t.Fatal("Int accepted a duration setting")
	}
}

// The registry must work against the real store, not only the fake: this is
// where the schema and the SQL are proven together.
func TestRegistryAgainstSQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "panel.db")

	db := openStore(t, path)
	r, err := New(db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s, err := r.Get(ctx, "panel.port"); err != nil || s.Value != "8443" || !s.IsDefault {
		t.Fatalf("Get before any write = %+v, %v; want the default", s, err)
	}

	var notified int
	r.Subscribe(func(Change) { notified++ })
	if err := r.Set(ctx, "panel.port", "9443"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if notified != 1 {
		t.Fatalf("subscriber ran %d times, want 1", notified)
	}

	// A second store on the same file sees the value: it is in SQLite, not in
	// the first registry's memory.
	reopened := openStore(t, path)
	second, err := New(reopened)
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	s, err := second.Get(ctx, "panel.port")
	if err != nil || s.Value != "9443" || s.IsDefault {
		t.Fatalf("Get from a fresh store = %+v, %v; want 9443", s, err)
	}
}
