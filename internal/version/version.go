// Package version carries the build stamp of the running binary.
package version

// Version is the release identifier, overridden at link time:
//
//	go build -ldflags "-X github.com/portbyte-id/server-panel/internal/version.Version=v0.1.0"
var Version = "dev"
