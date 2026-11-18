package nginx

import (
	"bytes"
	"strings"
	"testing"
)

const oldMain = "    server {\n        listen 80 default_server;\n        location / {\n            return 444;\n        }\n    }\n"

func TestUpgradeMainConfReplacesOldBlock(t *testing.T) {
	got, changed := UpgradeMainConf([]byte(oldMain), testPaths)
	if !changed {
		t.Fatal("old block not upgraded")
	}
	if strings.Contains(string(got), "return 444") || !strings.Contains(string(got), "No website is configured") {
		t.Fatalf("unexpected result:\n%s", got)
	}
	again, changed := UpgradeMainConf(got, testPaths)
	if changed || !bytes.Equal(again, got) {
		t.Fatal("upgrade is not idempotent")
	}
}

func TestUpgradeMainConfLeavesEditedBlockAlone(t *testing.T) {
	edited := []byte(strings.Replace(oldMain, "return 444;", "return 404;", 1))
	got, changed := UpgradeMainConf(edited, testPaths)
	if changed || !bytes.Equal(got, edited) {
		t.Fatal("operator-edited block was touched")
	}
}

// A configuration generated while nginx ran in a container names files that only
// the image had. The upgrade points them at the build.
func TestUpgradeMainConfMovesContainerPathsToTheBuild(t *testing.T) {
	old := "error_log /dev/stderr warn;\nhttp {\n    include /etc/nginx/mime.types;\n}\n"
	got, changed := UpgradeMainConf([]byte(old), testPaths)
	if !changed {
		t.Fatal("container paths were not upgraded")
	}
	for _, want := range []string{
		"error_log " + testPaths.MainErrorLog() + " warn;",
		"include " + testPaths.MimeTypes() + ";",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("result lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(string(got), "/etc/nginx") || strings.Contains(string(got), "/dev/stderr") {
		t.Errorf("a container path is left:\n%s", got)
	}
	// An operator who changed the line keeps their line.
	edited := []byte("error_log /var/log/mine.log warn;\ninclude /etc/nginx/mime.types.custom;\n")
	if again, changed := UpgradeMainConf(edited, testPaths); changed || !bytes.Equal(again, edited) {
		t.Errorf("an edited configuration was touched:\n%s", again)
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
