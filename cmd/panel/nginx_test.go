package main

import (
	"testing"

	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/nginx"
)

// The nginx package cannot import install (install imports the HTTP layer,
// which imports nginx), so it repeats the www account. The two must agree: the
// image's user and the owner of every web root are the same account.
func TestNginxAndInstallAgreeOnTheWWWAccount(t *testing.T) {
	if nginx.WWWUser != install.WWWUser || nginx.WWWUID != install.WWWUID || nginx.WWWGID != install.WWWGID {
		t.Fatalf("nginx has %s %d:%d, install has %s %d:%d",
			nginx.WWWUser, nginx.WWWUID, nginx.WWWGID, install.WWWUser, install.WWWUID, install.WWWGID)
	}
}
