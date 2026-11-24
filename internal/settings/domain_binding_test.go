package settings

import (
	"strings"
	"testing"
)

// validateBindingDomain decides what name the panel may be bound to. The empty
// string is the "no binding" value and must pass; an address and a bare label
// must not, because neither names something the operator can point DNS at.
func TestValidateBindingDomain(t *testing.T) {
	valid := []string{
		"",
		"panel.example.com",
		"Panel.Example.COM",
		"a-b.example.org",
		"my-panel2.corp.example.net",
	}
	for _, v := range valid {
		if err := validateBindingDomain(v); err != nil {
			t.Errorf("validateBindingDomain(%q) = %v, want nil", v, err)
		}
	}

	invalid := []string{
		"localhost",
		"192.168.1.5",
		"203.0.113.9",
		"::1",
		"-bad.example.com",
		"bad-.example.com",
		"bad_name.example.com",
		"a..example.com",
		"panel.example.com.",
		"sp ace.example.com",
		strings.Repeat("a", 64) + ".example.com",
	}
	for _, v := range invalid {
		if err := validateBindingDomain(v); err == nil {
			t.Errorf("validateBindingDomain(%q) = nil, want an error", v)
		}
	}
}

// The key is registered in the Panel group, optional, and empty by default, so
// a fresh install is not bound and the Settings page shows the field.
func TestPanelDomainKeyIsOptionalAndOffByDefault(t *testing.T) {
	cat, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	for _, g := range cat {
		for _, m := range g.Keys {
			if m.Name != KeyPanelDomain {
				continue
			}
			if m.Group != groupPanel {
				t.Errorf("group = %q, want %q", m.Group, groupPanel)
			}
			if m.Default != "" || !m.Optional {
				t.Errorf("default = %q optional = %v, want empty default and optional", m.Default, m.Optional)
			}
			return
		}
	}
	t.Fatalf("%s is not in the settings catalog", KeyPanelDomain)
}
