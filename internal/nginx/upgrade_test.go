package nginx

import (
	"bytes"
	"strings"
	"testing"
)

const oldMain = "    server {\n        listen 80 default_server;\n        location / {\n            return 444;\n        }\n    }\n"

func TestUpgradeMainConfReplacesOldBlock(t *testing.T) {
	got, changed := UpgradeMainConf([]byte(oldMain))
	if !changed {
		t.Fatal("old block not upgraded")
	}
	if strings.Contains(string(got), "return 444") || !strings.Contains(string(got), "No website is configured") {
		t.Fatalf("unexpected result:\n%s", got)
	}
	again, changed := UpgradeMainConf(got)
	if changed || !bytes.Equal(again, got) {
		t.Fatal("upgrade is not idempotent")
	}
}

func TestUpgradeMainConfLeavesEditedBlockAlone(t *testing.T) {
	edited := []byte(strings.Replace(oldMain, "return 444;", "return 404;", 1))
	got, changed := UpgradeMainConf(edited)
	if changed || !bytes.Equal(got, edited) {
		t.Fatal("operator-edited block was touched")
	}
}

func TestLandingPageIsSafeInsideNginxString(t *testing.T) {
	for _, bad := range []string{"$", "\\"} {
		if strings.Contains(landingLocation, bad) {
			t.Errorf("landing page contains %q", bad)
		}
	}
	start := strings.Index(landingLocation, "'")
	if start < 0 || strings.Count(landingLocation, "'") != 2 {
		t.Fatalf("landing page must hold exactly the two quotes of its nginx string")
	}
}
