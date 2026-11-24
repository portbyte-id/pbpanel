package main

import (
	"context"
	"fmt"
	"io"

	"github.com/portbyte-id/server-panel/internal/settings"
)

// removeDomainBinding clears panel.domain, so the panel answers on every host
// name again. It is a console command because a binding can lock the operator
// out of the Settings page that would clear it. The running panel reads the
// setting on every request, so no restart is needed.
func removeDomainBinding(ctx context.Context, w io.Writer, path string) error {
	st, err := openStore(path)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	reg, err := settings.New(st)
	if err != nil {
		return err
	}

	prev, err := reg.Str(ctx, settings.KeyPanelDomain)
	if err == nil && prev == "" {
		printf(w, "No domain binding is set: the panel already answers on every host name.\n")
		return nil
	}

	// The stored value is not validated on the way out: a binding whose row no
	// longer passes the validator must still be removable, which is the point.
	if err := reg.Set(ctx, settings.KeyPanelDomain, ""); err != nil {
		return fmt.Errorf("remove domain binding: %w", err)
	}
	if prev != "" {
		printf(w, "Domain binding to %s removed. The panel answers on every host name again.\n", prev)
	} else {
		printf(w, "Domain binding removed. The panel answers on every host name again.\n")
	}
	return nil
}
