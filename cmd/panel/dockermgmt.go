package main

import (
	"log/slog"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/httpapi"
)

// newDockerMgmt returns the Docker page's service, or nil when the client cannot
// be built, which leaves /api/docker answering 503. docker.New does not dial, so
// a host where Docker is stopped still gets a service; its calls fail with
// "Docker is not reachable", which is the message the page should show.
//
// It returns the interface, not *docker.Client: a nil *docker.Client stored in
// the interface would compare non-nil and the routes would never answer 503.
func newDockerMgmt(logger *slog.Logger) httpapi.DockerService {
	c, err := docker.New(docker.DefaultSocket)
	if err != nil {
		logger.Warn("docker management is not available", "error", err)
		return nil
	}
	return c
}
