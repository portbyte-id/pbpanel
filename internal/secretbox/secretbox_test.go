package secretbox

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func newBox(t *testing.T, fill byte) *Box {
	t.Helper()
	b, err := New(bytes.Repeat([]byte{fill}, MasterKeySize))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := newBox(t, 1)
	for _, plain := range [][]byte{[]byte("cloudflare-token"), {}, bytes.Repeat([]byte{0}, 4096)} {
		sealed, err := b.Seal(plain, "dns_providers:cf")
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		if !strings.HasPrefix(sealed, "v1:"+b.KeyID()+":") {
			t.Fatalf("sealed %q lacks the version and key ID prefix", sealed)
		}
		got, err := b.Open(sealed, "dns_providers:cf")
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("Open = %q, %v; want %q", got, err, plain)
		}
	}
}

func TestSealDoesNotLeakPlaintext(t *testing.T) {
	sealed, _ := newBox(t, 1).Seal([]byte("super-secret-token"), "a")
	if strings.Contains(sealed, "super-secret-token") {
		t.Fatal("plaintext appears in the sealed value")
	}
}

func TestNewRejectsWrongKeySize(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		if _, err := New(make([]byte, n)); err == nil {
			t.Fatalf("New accepted a %d-byte key", n)
		}
	}
}

func TestNoncesAreUnique(t *testing.T) {
	b := newBox(t, 1)
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		s, err := b.Seal([]byte("same"), "same")
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		if seen[s] {
			t.Fatal("two seals of the same input were identical")
		}
		seen[s] = true
	}
}

func TestTamperDetection(t *testing.T) {
	b := newBox(t, 1)
	sealed, _ := b.Seal([]byte("token"), "aad")
	head := strings.LastIndex(sealed, ":") + 1

	// Flip every payload character in turn: each must be refused.
	for i := head; i < len(sealed); i++ {
		c := byte('A')
		if sealed[i] == 'A' {
			c = 'B'
		}
		mut := sealed[:i] + string(c) + sealed[i+1:]
		if _, err := b.Open(mut, "aad"); !errors.Is(err, ErrTampered) {
			t.Fatalf("flip at %d: err = %v, want ErrTampered", i, err)
		}
	}

	for name, in := range map[string]string{
		"empty":     "",
		"no parts":  "garbage",
		"bad ver":   strings.Replace(sealed, "v1:", "v9:", 1),
		"truncated": sealed[:head+4],
		"not b64":   sealed[:head] + "!!!!",
	} {
		if _, err := b.Open(in, "aad"); !errors.Is(err, ErrTampered) {
			t.Fatalf("%s: err = %v, want ErrTampered", name, err)
		}
	}
}

func TestWrongAADIsRefused(t *testing.T) {
	b := newBox(t, 1)
	sealed, _ := b.Seal([]byte("token"), "dns_providers:a")
	if _, err := b.Open(sealed, "dns_providers:b"); !errors.Is(err, ErrTampered) {
		t.Fatalf("err = %v, want ErrTampered", err)
	}
}

func TestWrongKeyIsReportedAsUnknownKey(t *testing.T) {
	sealed, _ := newBox(t, 1).Seal([]byte("token"), "aad")
	other := newBox(t, 2)
	if other.KeyID() == newBox(t, 1).KeyID() {
		t.Fatal("distinct master keys produced one key ID")
	}
	if _, err := other.Open(sealed, "aad"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}
}

func TestKeyIDIsStable(t *testing.T) {
	if newBox(t, 7).KeyID() != newBox(t, 7).KeyID() {
		t.Fatal("the same master key gave two key IDs")
	}
}
