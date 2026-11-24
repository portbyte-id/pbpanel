package main

import (
	"errors"
	"os/user"
	"testing"

	"github.com/portbyte-id/server-panel/internal/nginx"
)

// The challenge socket must be owned by the group the www account actually has:
// on a host where www is gid 1000, the default 1500 leaves nginx's workers refused.
func TestWWWGroupIDFollowsTheAccount(t *testing.T) {
	found := func(string) (*user.User, error) { return &user.User{Uid: "1000", Gid: "1000"}, nil }
	if got := wwwGroupID(found); got != 1000 {
		t.Fatalf("gid = %d, want the account's 1000", got)
	}
	missing := func(string) (*user.User, error) { return nil, errors.New("unknown user") }
	if got := wwwGroupID(missing); got != nginx.WWWGID {
		t.Fatalf("no account: gid = %d, want the default %d", got, nginx.WWWGID)
	}
	junk := func(string) (*user.User, error) { return &user.User{Gid: "not-a-number"}, nil }
	if got := wwwGroupID(junk); got != nginx.WWWGID {
		t.Fatalf("bad gid: %d, want the default %d", got, nginx.WWWGID)
	}
}
