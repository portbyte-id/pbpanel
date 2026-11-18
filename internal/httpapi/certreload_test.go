package httpapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// issueCert returns a PEM pair for 127.0.0.1 with the given serial and validity.
func issueCert(t *testing.T, serial int64, notBefore, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "replacement.test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pemEncode("CERTIFICATE", der), pemEncode("EC PRIVATE KEY", keyDER)
}

func validCert(t *testing.T, serial int64) ([]byte, []byte) {
	t.Helper()
	return issueCert(t, serial, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
}

// dialSerial opens a fresh handshake to port and returns the served leaf serial.
func dialSerial(t *testing.T, port int) int64 {
	t.Helper()
	conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		&tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
}

func startReloadListener(t *testing.T) (*Listener, CertPaths) {
	t.Helper()
	l := newTestListener(t, 50*time.Millisecond)
	if err := l.Start(0); err != nil {
		t.Fatal(err)
	}
	return l, l.certs.paths
}

func TestReplaceServesNewCertWithoutRestart(t *testing.T) {
	l, _ := startReloadListener(t)
	oldSerial := dialSerial(t, l.Port())

	// An established keepalive connection must survive the swap.
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	defer client.CloseIdleConnections()
	url := "https://127.0.0.1:" + strconv.Itoa(l.Port())
	get := func() {
		t.Helper()
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		_ = resp.Body.Close()
	}
	get()

	certPEM, keyPEM := validCert(t, 4242)
	if err := l.Certs().Replace(certPEM, keyPEM, nil); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	if got := dialSerial(t, l.Port()); got != 4242 || got == oldSerial {
		t.Errorf("served serial after Replace = %d, want 4242 (was %d)", got, oldSerial)
	}
	get() // same kept-alive connection
	info := l.Certs().Current()
	if info.Serial != "1092" || info.Subject != "replacement.test" || !info.SelfSigned {
		t.Errorf("Current = %+v", info)
	}
	if len(info.SANs) != 1 || info.SANs[0] != "127.0.0.1" {
		t.Errorf("SANs = %v", info.SANs)
	}
}

func TestFailedReplaceKeepsServingOldCert(t *testing.T) {
	l, paths := startReloadListener(t)
	oldSerial := dialSerial(t, l.Port())
	goodCert, _ := validCert(t, 7)
	_, otherKey := validCert(t, 8)
	expCert, expKey := issueCert(t, 9, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	futCert, futKey := issueCert(t, 10, time.Now().Add(24*time.Hour), time.Now().Add(48*time.Hour))

	cases := map[string]struct{ cert, key []byte }{
		"mismatched key": {goodCert, otherKey},
		"expired":        {expCert, expKey},
		"not yet valid":  {futCert, futKey},
		"garbage":        {[]byte("nope"), []byte("nope")},
		"empty":          {nil, nil},
	}
	before, _ := os.ReadFile(paths.CertFile)
	for name, c := range cases {
		if err := l.Certs().Replace(c.cert, c.key, nil); err == nil {
			t.Errorf("%s: Replace succeeded", name)
		}
		if got := dialSerial(t, l.Port()); got != oldSerial {
			t.Errorf("%s: served %d, want old %d", name, got, oldSerial)
		}
	}
	after, _ := os.ReadFile(paths.CertFile)
	if string(before) != string(after) {
		t.Error("a refused Replace modified the certificate file")
	}
	if _, err := os.Stat(paths.CertFile + prevSuffix); err == nil {
		t.Error("a refused Replace wrote a .prev copy")
	}
}

func TestReplaceKeepsPreviousPair(t *testing.T) {
	l, paths := startReloadListener(t)
	oldPEM, _ := os.ReadFile(paths.CertFile)
	certPEM, keyPEM := validCert(t, 11)
	if err := l.Certs().Replace(certPEM, keyPEM, nil); err != nil {
		t.Fatal(err)
	}
	prev, err := os.ReadFile(paths.CertFile + prevSuffix)
	if err != nil || string(prev) != string(oldPEM) {
		t.Fatalf("panel.crt.prev is not the displaced certificate (err %v)", err)
	}
	if _, err := tls.LoadX509KeyPair(paths.CertFile+prevSuffix, paths.KeyFile+prevSuffix); err != nil {
		t.Errorf("rollback pair does not load: %v", err)
	}
}

// TestReloadBadPairKeepsOld: a pair corrupted on disk is refused and the live
// certificate stays.
func TestReloadBadPairKeepsOld(t *testing.T) {
	l, paths := startReloadListener(t)
	oldSerial := dialSerial(t, l.Port())
	if err := os.WriteFile(paths.CertFile, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := l.Certs().Reload(); err == nil {
		t.Fatal("Reload accepted a corrupt certificate")
	}
	if got := dialSerial(t, l.Port()); got != oldSerial {
		t.Errorf("served %d after a failed Reload, want %d", got, oldSerial)
	}
}

// TestStartupRepairsCrashMidReplace simulates a crash between the two renames:
// new key, old certificate. The next start restores the rollback pair.
func TestStartupRepairsCrashMidReplace(t *testing.T) {
	paths := certPathsIn(t)
	if err := GenerateSelfSigned(paths, []string{"127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	r, err := NewCertReloader(paths)
	if err != nil {
		t.Fatal(err)
	}
	oldSerial := r.Current().Serial
	certPEM, keyPEM := validCert(t, 12)
	if err := r.Replace(certPEM, keyPEM, nil); err != nil {
		t.Fatal(err)
	}
	// Crash state: the new key is in place but the certificate is the old one.
	prevCert, _ := os.ReadFile(paths.CertFile + prevSuffix)
	if err := os.WriteFile(paths.CertFile, prevCert, 0o644); err != nil {
		t.Fatal(err)
	}

	r2, err := NewCertReloader(paths)
	if err != nil {
		t.Fatalf("NewCertReloader after crash: %v", err)
	}
	if got := r2.Current().Serial; got != oldSerial {
		t.Errorf("recovered serial %s, want the rollback %s", got, oldSerial)
	}
}

func TestStartupWithoutFilesStillFails(t *testing.T) {
	if _, err := NewCertReloader(certPathsIn(t)); err == nil {
		t.Fatal("NewCertReloader succeeded with no certificate")
	}
}

func TestConcurrentHandshakesDuringReload(t *testing.T) {
	l, _ := startReloadListener(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(l.Port())),
					&tls.Config{InsecureSkipVerify: true})
				if err != nil {
					t.Errorf("handshake failed during reload: %v", err)
					return
				}
				_ = l.Certs().Current()
				_ = conn.Close()
			}
		}()
	}
	for i := int64(0); i < 10; i++ {
		certPEM, keyPEM := validCert(t, 100+i)
		if err := l.Certs().Replace(certPEM, keyPEM, nil); err != nil {
			t.Errorf("Replace: %v", err)
		}
		_ = l.Certs().Reload()
	}
	close(stop)
	wg.Wait()
}

func TestRebindStillServesReloadedCert(t *testing.T) {
	l, _ := startReloadListener(t)
	certPEM, keyPEM := validCert(t, 555)
	if err := l.Certs().Replace(certPEM, keyPEM, nil); err != nil {
		t.Fatal(err)
	}
	// Port 0 differs from the current port, so this binds a fresh one.
	if _, err := l.ChangePort(0); err != nil {
		t.Fatal(err)
	}
	if got := dialSerial(t, l.Port()); got != 555 {
		t.Errorf("rebound listener serves %d, want 555", got)
	}
	c2, k2 := validCert(t, 556)
	if err := l.Certs().Replace(c2, k2, nil); err != nil {
		t.Fatal(err)
	}
	if got := dialSerial(t, l.Port()); got != 556 {
		t.Errorf("after rebind and replace serves %d, want 556", got)
	}
}
