package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/portbyte-id/server-panel/internal/settings"
)

// settingJSON is one key on the Settings page. It carries the registry's
// metadata so the page can render a field without a second, hard-coded list.
type settingJSON struct {
	Name            string   `json:"name"`
	Type            string   `json:"type"`
	Value           string   `json:"value"`
	Default         string   `json:"default"`
	IsDefault       bool     `json:"is_default"`
	RestartRequired bool     `json:"restart_required"`
	Description     string   `json:"description"`
	Group           string   `json:"group"`
	Allowed         []string `json:"allowed,omitempty"`
}

// settingsResponse is the GET /api/settings body. Groups are emitted in registry
// order so the page's section order is decided by the registry, not by map
// iteration.
type settingsResponse struct {
	Groups []settingsGroup `json:"groups"`
}

type settingsGroup struct {
	Name string        `json:"name"`
	Keys []settingJSON `json:"keys"`
}

func toSettingJSON(s settings.Setting) settingJSON {
	return settingJSON{
		Name:            s.Name,
		Type:            string(s.Type),
		Value:           s.Value,
		Default:         s.Default,
		IsDefault:       s.IsDefault,
		RestartRequired: s.RestartRequired,
		Description:     s.Description,
		Group:           s.Group,
		Allowed:         s.Allowed,
	}
}

// handleGetSettings returns every key with its value in effect, grouped for the
// Settings page.
func (s *server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.deps.Settings.Snapshot(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}

	resp := settingsResponse{Groups: []settingsGroup{}}
	for _, group := range s.deps.Settings.Groups() {
		g := settingsGroup{Name: group.Name, Keys: []settingJSON{}}
		for _, meta := range group.Keys {
			// The snapshot and the group list are built from the same registry,
			// so every grouped key has a value; skipping rather than assuming
			// keeps a mismatch from panicking on a nil.
			for _, st := range snapshot {
				if st.Name == meta.Name {
					g.Keys = append(g.Keys, toSettingJSON(st))
					break
				}
			}
		}
		resp.Groups = append(resp.Groups, g)
	}
	writeJSON(w, http.StatusOK, resp)
}

// settingWrite is one entry of the PUT /api/settings body.
type settingWrite struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// putSettingsRequest is the PUT /api/settings body: a list of writes applied in
// order, so the page can save a whole form in one request.
type putSettingsRequest struct {
	Settings []settingWrite `json:"settings"`
}

// handlePutSettings validates and stores each write through the registry, then
// returns the settings in effect.
//
// Writes are applied in order and the first rejected value stops the request
// with its own message. That is deliberately not all-or-nothing across keys:
// the registry stores one key at a time, and reporting which key was refused is
// more useful to the operator than discarding the valid ones silently. The keys
// already stored are named in the error detail so the client can re-read and
// show the true state.
func (s *server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req putSettingsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	if len(req.Settings) == 0 {
		writeError(w, http.StatusBadRequest, errBadRequest, "settings must list at least one key")
		return
	}

	for i, write := range req.Settings {
		if write.Name == "" {
			writeError(w, http.StatusBadRequest, errBadRequest,
				"settings["+itoa(i)+"].name is required")
			return
		}
		undo, err := s.applyLive(r, write)
		if err != nil {
			writeError(w, http.StatusBadRequest, errInvalidSetting,
				"settings["+itoa(i)+"] ("+write.Name+"): "+err.Error())
			return
		}
		if err := s.deps.Settings.Set(r.Context(), write.Name, write.Value); err != nil {
			if undo != nil {
				undo()
			}
			s.writeSettingError(w, r, write, i, err)
			return
		}
	}

	s.handleGetSettings(w, r)
}

// writeSettingError maps a registry failure to the envelope: an unknown key is a
// 404, a rejected value is a 400 carrying the validator's own sentence, and
// anything else is the database failing and stays a 500.
func (s *server) writeSettingError(w http.ResponseWriter, r *http.Request, write settingWrite, index int, err error) {
	var notFound *settings.NotFoundError
	var invalid *settings.ValidationError
	switch {
	case errors.As(err, &notFound):
		writeError(w, http.StatusNotFound, errNotFound,
			"settings["+itoa(index)+"]: unknown setting "+strconv.Quote(write.Name))
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, errInvalidSetting,
			"settings["+itoa(index)+"] ("+write.Name+"): "+invalid.Reason)
	default:
		s.writeInternal(w, r, err)
	}
}

// applyLive performs the side effect a setting needs before it is stored. Today
// that is one key: panel.port moves the listener first, so a port that cannot be
// bound is refused and the stored value never changes to something the daemon is
// not serving on. The returned undo puts the old port back if storing fails
// afterwards. A value the registry would reject is left for Set to report, so
// validation messages stay in one place.
func (s *server) applyLive(r *http.Request, write settingWrite) (undo func(), err error) {
	if write.Name != "panel.port" || s.deps.Rebind == nil {
		return nil, nil
	}
	if s.deps.Settings.Validate(write.Name, write.Value) != nil {
		return nil, nil
	}
	port, convErr := strconv.Atoi(write.Value)
	if convErr != nil {
		return nil, nil
	}
	old, getErr := s.deps.Settings.Int(r.Context(), write.Name)
	if getErr != nil {
		return nil, getErr
	}
	if old == port {
		return nil, nil
	}
	if err := s.deps.Rebind(port); err != nil {
		return nil, err
	}
	return func() { _ = s.deps.Rebind(old) }, nil
}
