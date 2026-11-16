package httpapi

import (
	"net/http"

	"github.com/portbyte-id/server-panel/internal/version"
)

// healthResponse is what GET /api/health answers.
type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// handleHealth reports that this binary is serving.
//
// It is deliberately the cheapest handler in the panel: no session, no database
// read, no file. `panel upgrade` restarts the service and polls this until it
// answers, so a handler that touched the database would turn "the new binary is
// healthy" into "the database happened to be free", and a rollback decision
// would be made on the wrong signal.
//
// The version in the body is what an operator sees when checking by hand that
// the swap took effect.
func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Version: version.Version})
}
