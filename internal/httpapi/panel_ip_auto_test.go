package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	tlspkg "github.com/portbyte-id/server-panel/internal/tls"
)

func publicAt(ip string) func() (string, bool) {
	return func() (string, bool) { return ip, ip != "" }
}

func TestAutoIssuePanelIPBehindNATOrdersNothing(t *testing.T) {
	h := newTLSHarness(t)
	l := h.withPanel()
	if err := l.Start(0); err != nil {
		t.Fatal(err)
	}
	res := h.td.AutoIssuePanelIP(context.Background(), publicAt(""), "")
	if res.Outcome != AutoIPSkipped {
		t.Fatalf("res = %+v", res)
	}
	if len(h.issuer.calls) != 0 {
		t.Errorf("the CA was asked: %v", h.issuer.calls)
	}
}

func TestAutoIssuePanelIPReplacesTheSelfSignedCertificate(t *testing.T) {
	h := newTLSHarness(t)
	l := h.withPanel()
	if err := l.Start(0); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	res := h.td.AutoIssuePanelIP(ctx, publicAt("45.76.10.4"), "")
	if res.Outcome != AutoIPIssued || res.IP != "45.76.10.4" {
		t.Fatalf("res = %+v", res)
	}
	if want := []string{"http01 45.76.10.4 "}; !slices.Equal(h.issuer.calls, want) {
		t.Errorf("issuer calls = %q, want %q", h.issuer.calls, want)
	}
	if !slices.Equal(h.issuer.names[0], []string{"45.76.10.4"}) {
		t.Errorf("names = %v", h.issuer.names[0])
	}
	if d := h.td.PanelDomain(); d != "45.76.10.4" {
		t.Errorf("panel domain marker = %q", d)
	}
	if got := dialSerial(t, l.Port()); got != 777 {
		t.Errorf("the panel serves serial %d, want the issued 777", got)
	}

	// Already managed: the renewer's job, not another order.
	if res := h.td.AutoIssuePanelIP(ctx, publicAt("45.76.10.4"), ""); res.Outcome != AutoIPSkipped {
		t.Errorf("second pass = %+v", res)
	}
	if len(h.issuer.calls) != 1 {
		t.Errorf("issuer calls = %v", h.issuer.calls)
	}
}

func TestAutoIssuePanelIPFollowsAnAddressChange(t *testing.T) {
	h := newTLSHarness(t)
	l := h.withPanel()
	if err := l.Start(0); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if res := h.td.AutoIssuePanelIP(ctx, publicAt("45.76.10.4"), ""); res.Outcome != AutoIPIssued {
		t.Fatalf("first = %+v", res)
	}
	if res := h.td.AutoIssuePanelIP(ctx, publicAt("45.76.10.9"), ""); res.Outcome != AutoIPIssued {
		t.Fatalf("second = %+v", res)
	}
	if d := h.td.PanelDomain(); d != "45.76.10.9" {
		t.Errorf("panel domain marker = %q", d)
	}
	if _, err := h.cs.GetCert(ctx, "45.76.10.4"); !errors.Is(err, tlspkg.ErrNotFound) {
		t.Errorf("the old address's certificate is still renewed: %v", err)
	}
}

func TestAutoIssuePanelIPLeavesAnUploadedCertificateAlone(t *testing.T) {
	h := newTLSHarness(t)
	l := h.withPanel()
	if err := l.Start(0); err != nil {
		t.Fatal(err)
	}
	c, k := goodCert(t, 4711, "panel.test")
	if code, _ := h.call(http.MethodPost, "/api/panel/cert/upload", map[string]any{"certificate": c, "private_key": k}); code != 200 {
		t.Fatalf("upload = %d", code)
	}
	res := h.td.AutoIssuePanelIP(context.Background(), publicAt("45.76.10.4"), "")
	if res.Outcome != AutoIPSkipped {
		t.Fatalf("res = %+v", res)
	}
	if len(h.issuer.calls) != 0 {
		t.Errorf("the CA was asked: %v", h.issuer.calls)
	}
}

func TestAutoIssuePanelIPLeavesAHostnameCertificateAlone(t *testing.T) {
	h := newTLSHarness(t)
	l := h.withPanel()
	if err := l.Start(0); err != nil {
		t.Fatal(err)
	}
	if err := h.td.setPanelDomain("panel.test"); err != nil {
		t.Fatal(err)
	}
	res := h.td.AutoIssuePanelIP(context.Background(), publicAt("45.76.10.4"), "")
	if res.Outcome != AutoIPSkipped {
		t.Fatalf("res = %+v", res)
	}
	if len(h.issuer.calls) != 0 {
		t.Errorf("the CA was asked: %v", h.issuer.calls)
	}
}

func TestAutoIssuePanelIPReportsACAFailure(t *testing.T) {
	h := newTLSHarness(t)
	l := h.withPanel()
	if err := l.Start(0); err != nil {
		t.Fatal(err)
	}
	h.issuer.err = errors.New("tls: order for 45.76.10.4: connection refused")
	res := h.td.AutoIssuePanelIP(context.Background(), publicAt("45.76.10.4"), "")
	if res.Outcome != AutoIPFailed || res.Reason == "" {
		t.Fatalf("res = %+v", res)
	}
	if d := h.td.PanelDomain(); d != "" {
		t.Errorf("a failed order set the marker to %q", d)
	}
}
