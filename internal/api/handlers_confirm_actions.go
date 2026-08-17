package api

import (
	"context"
	"net/http"
)

// Confirmation prompts before stopping or restarting a server.
//
// Stop and Restart are the two controls that act on a LIVE server the instant
// they are pressed: players are disconnected, an app drops its connections, and
// there is no undo — the server can be started again but the interruption
// already happened. Every other action of that weight in the panel (wipe,
// delete, reinstall, safe restart) already asks first; these two did not, and
// they sit next to each other in the action bar and in the bulk toolbar, where a
// mis-click lands on a running production server.
//
// It is a guard against accidents, not a permission: the prompt lives in the UI,
// so the API still stops a server when told to (which is what the Telegram bot,
// schedules and Kvasir's own confirmed actions rely on). Anyone who should not be
// able to stop a server is stopped by RBAC, not by this.
//
// Default ON — an unset value reads as enabled, so existing installs get the
// prompt without anyone visiting Settings. Turning it off is one switch, for
// whoever finds the extra click more annoying than a stray stop.

// confirmActionsEnabled reports whether the UI should ask before a stop or a
// restart. Unset means on; only an explicit "0" turns it off.
func (s *Server) confirmActionsEnabled(ctx context.Context) bool {
	return s.getSetting(ctx, "confirm_actions") != "0"
}

func (s *Server) handleGetConfirmActions(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{"enabled": s.confirmActionsEnabled(r.Context())})
}

func (s *Server) handleSetConfirmActions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Enabled != nil {
		// Stored as "1"/"0" rather than deleted when on, so "on" is a recorded
		// decision and not indistinguishable from a fresh install.
		s.setSetting(r.Context(), "confirm_actions", boolStr(*req.Enabled))
		s.auditLog(r, "settings.confirm_actions", "confirm_actions", map[string]any{"enabled": *req.Enabled})
	}
	s.handleGetConfirmActions(w, r)
}
