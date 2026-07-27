package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// handleServerActivity surfaces the panel's own recorded history for one server —
// named player sessions (who was on, when) and notable security/health events
// (WordPress xmlrpc/login attempts, HTTP 5xx, …) — so the intelligence Kvasir can
// answer in chat is also visible in the UI. Read-only, ServerView-gated, last N
// hours (default 7 days, max 30). Both lists are empty for servers whose rune
// records neither, which the UI uses to hide the tab.
func (s *Server) handleServerActivity(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerView, s.serverTarget(r.Context(), id)) {
		return
	}
	hours := 168
	if h := r.URL.Query().Get("hours"); h != "" {
		if n, err := strconv.Atoi(h); err == nil && n > 0 && n <= 720 {
			hours = n
		}
	}
	win := fmt.Sprintf("-%d hours", hours)

	type session struct {
		Name     string `json:"name"`
		JoinedAt string `json:"joined_at"`
		LeftAt   string `json:"left_at"`
	}
	type event struct {
		Key     string `json:"key"`
		Label   string `json:"label"`
		Subject string `json:"subject"`
		Count   int    `json:"count"`
		LastTS  string `json:"last_ts"`
	}
	resp := struct {
		Sessions []session `json:"sessions"`
		Events   []event   `json:"events"`
		Hours    int       `json:"hours"`
	}{Sessions: []session{}, Events: []event{}, Hours: hours}

	if rows, err := s.db.QueryContext(r.Context(),
		`SELECT player_name, joined_at, COALESCE(left_at,'')
		 FROM player_sessions WHERE server_id=? AND joined_at >= datetime('now', ?)
		 ORDER BY joined_at DESC LIMIT 200`, id, win); err == nil {
		for rows.Next() {
			var e session
			if rows.Scan(&e.Name, &e.JoinedAt, &e.LeftAt) == nil {
				resp.Sessions = append(resp.Sessions, e)
			}
		}
		rows.Close()
	}

	if rows, err := s.db.QueryContext(r.Context(),
		`SELECT key, label, subject, SUM(count) AS c, MAX(last_ts)
		 FROM app_events WHERE server_id=? AND bucket >= datetime('now', ?)
		 GROUP BY key, subject ORDER BY c DESC LIMIT 200`, id, win); err == nil {
		for rows.Next() {
			var e event
			if rows.Scan(&e.Key, &e.Label, &e.Subject, &e.Count, &e.LastTS) == nil {
				resp.Events = append(resp.Events, e)
			}
		}
		rows.Close()
	}

	jsonOK(w, resp)
}
