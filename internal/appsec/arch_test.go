package appsec

import (
	"os"
	"testing"
)

// TestMain pins the architecture to amd64, the only one the vendor publishes,
// so the install tests do not depend on the machine that runs them.
func TestMain(m *testing.M) {
	goarch = "amd64"
	os.Exit(m.Run())
}

func TestArchitectureRefusesArm64(t *testing.T) {
	goarch = "arm64"
	t.Cleanup(func() { goarch = "amd64" })
	if a, err := Architecture(); err == nil {
		t.Fatalf("arm64 must be refused, got %q", a)
	}
}
