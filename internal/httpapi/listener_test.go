package httpapi

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

func newTestListener(t *testing.T, grace time.Duration) *Listener {
	t.Helper()
	paths := certPathsIn(t)
	if err := GenerateSelfSigned(paths, []string{"127.0.0.1"}); err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	l, err := NewListener(h, paths, "127.0.0.1", grace)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = l.Close(ctx)
	})
	return l
}

// answers reports whether an HTTPS GET to port succeeds. A fresh transport per
// call, so a kept-alive connection to the old listener cannot fake an answer.
func answers(port int) bool {
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	resp, err := c.Get("https://127.0.0.1:" + strconv.Itoa(port) + "/")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestChangePortServesNewThenClosesOld is the 0.8 integration test on two free
// ports: the new one answers at once, the old one stops after the grace period.
func TestChangePortServesNewThenClosesOld(t *testing.T) {
	l := newTestListener(t, 200*time.Millisecond)
	oldPort, newPort := freePort(t), freePort(t)
	if err := l.Start(oldPort); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !answers(oldPort) {
		t.Fatalf("old port %d does not answer before the change", oldPort)
	}

	url, err := l.ChangePort(newPort)
	if err != nil {
		t.Fatalf("ChangePort: %v", err)
	}
	if want := "https://127.0.0.1:" + strconv.Itoa(newPort); url != want {
		t.Errorf("url = %q, want %q", url, want)
	}
	if !answers(newPort) {
		t.Fatalf("new port %d does not answer right after the change", newPort)
	}
	waitFor(t, "the old port to stop answering after the grace period", func() bool { return !answers(oldPort) })
	if !answers(newPort) {
		t.Fatalf("new port stopped answering")
	}
}

// TestChangePortToABusyPortKeepsTheOldListener covers the lock-out case: the
// bind fails, the error comes back, and the panel keeps serving where it was.
func TestChangePortToABusyPortKeepsTheOldListener(t *testing.T) {
	l := newTestListener(t, 50*time.Millisecond)
	oldPort := freePort(t)
	if err := l.Start(oldPort); err != nil {
		t.Fatalf("Start: %v", err)
	}

	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	busyPort := busy.Addr().(*net.TCPAddr).Port

	if _, err := l.ChangePort(busyPort); err == nil {
		t.Fatal("ChangePort onto a busy port succeeded")
	}
	time.Sleep(200 * time.Millisecond) // longer than the grace period
	if !answers(oldPort) {
		t.Fatal("the old listener stopped serving after a failed change")
	}
	if got := l.Port(); got != oldPort {
		t.Errorf("Port() = %d after a failed change, want %d", got, oldPort)
	}
}

// TestConcurrentChangesAreSerialised proves racing changes leave exactly one
// live listener: once the grace passes only the final port answers.
func TestConcurrentChangesAreSerialised(t *testing.T) {
	l := newTestListener(t, 100*time.Millisecond)
	start := freePort(t)
	if err := l.Start(start); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Reserve distinct ports by holding them open until all are chosen.
	var holders []net.Listener
	var ports []int
	for range 4 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		holders = append(holders, ln)
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
	}
	for _, ln := range holders {
		_ = ln.Close()
	}

	var wg sync.WaitGroup
	for _, p := range ports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = l.ChangePort(p)
		}()
	}
	wg.Wait()

	final := l.Port()
	waitFor(t, "only the final port to answer", func() bool {
		if !answers(final) {
			return false
		}
		for _, p := range append(ports, start) {
			if p != final && answers(p) {
				return false
			}
		}
		return true
	})
}

func TestChangePortAfterCloseFails(t *testing.T) {
	l := newTestListener(t, 50*time.Millisecond)
	if err := l.Start(freePort(t)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := l.ChangePort(freePort(t)); err == nil {
		t.Fatal("ChangePort after Close succeeded")
	}
}
