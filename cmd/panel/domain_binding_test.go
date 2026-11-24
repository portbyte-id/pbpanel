package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/settings"
)

// storedPanelDomain reads panel.domain the way the daemon does: through the
// registry, against the same database the console command wrote to.
func storedPanelDomain(t *testing.T, path string) string {
	t.Helper()
	st, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer func() { _ = st.Close() }()

	reg, err := settings.New(st)
	if err != nil {
		t.Fatalf("settings.New: %v", err)
	}
	v, err := reg.Str(context.Background(), settings.KeyPanelDomain)
	if err != nil {
		t.Fatalf("Str: %v", err)
	}
	return v
}

func TestRemoveDomainBindingClearsTheBinding(t *testing.T) {
	tempDB(t)
	ctx := context.Background()
	path := databasePath()

	if err := setSetting(ctx, io.Discard, path, settings.KeyPanelDomain, "panel.example.com"); err != nil {
		t.Fatalf("setSetting: %v", err)
	}

	var out bytes.Buffer
	if err := removeDomainBinding(ctx, &out, path); err != nil {
		t.Fatalf("removeDomainBinding: %v", err)
	}
	if got := storedPanelDomain(t, path); got != "" {
		t.Fatalf("stored domain = %q after remove, want empty", got)
	}
	if !strings.Contains(out.String(), "panel.example.com") || !strings.Contains(out.String(), "removed") {
		t.Fatalf("output = %q, want it to name the removed domain", out.String())
	}
}

func TestRemoveDomainBindingWhenNoneIsSetSaysSo(t *testing.T) {
	tempDB(t)

	var out bytes.Buffer
	if err := removeDomainBinding(context.Background(), &out, databasePath()); err != nil {
		t.Fatalf("removeDomainBinding: %v", err)
	}
	if !strings.Contains(out.String(), "No domain binding is set") {
		t.Fatalf("output = %q, want the no-binding notice", out.String())
	}
}

// The command is reachable as `pbp domain-binding remove` and takes no arguments.
func TestDomainBindingRemoveIsDispatched(t *testing.T) {
	sel, err := selectCommand([]string{"domain-binding", "remove"}, commandSet(io.Discard))
	if err != nil {
		t.Fatalf("selectCommand: %v", err)
	}
	if sel.cmd == nil || sel.cmd.name != "domain-binding remove" {
		t.Fatalf("selected %+v, want the domain-binding remove command", sel.cmd)
	}
	if len(sel.args) != 0 {
		t.Fatalf("args = %v, want none", sel.args)
	}
}
