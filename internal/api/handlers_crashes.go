package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

type crashEvent struct {
	TS       string `json:"ts"`
	ExitCode int    `json:"exit_code"`
	Reason   string `json:"reason"`
}

// handleServerCrashes returns a server's recent unexpected exits (crash history)
// over the last N hours (default 7 days, max 30), newest first.
func (s *Server) handleServerCrashes(w http.ResponseWriter, r *http.Request) {
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
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT ts, exit_code, reason FROM server_crashes WHERE server_id=? AND ts >= datetime('now', ?) ORDER BY ts DESC LIMIT 50",
		id, fmt.Sprintf("-%d hours", hours))
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := []crashEvent{}
	for rows.Next() {
		var e crashEvent
		if rows.Scan(&e.TS, &e.ExitCode, &e.Reason) == nil {
			list = append(list, e)
		}
	}
	jsonOK(w, list)
}

// handleCrashesSummary returns a {server_id: count} map of unexpected exits in the
// last N hours (default 24, max 30 days) across every server — the source for the
// "flapping"/"crashed" badges on the server list and dashboard. Any authed user;
// the frontend only shows badges for servers it already lists.
func (s *Server) handleCrashesSummary(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		if n, err := strconv.Atoi(h); err == nil && n > 0 && n <= 720 {
			hours = n
		}
	}
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT server_id, COUNT(*) FROM server_crashes WHERE ts >= datetime('now', ?) GROUP BY server_id",
		fmt.Sprintf("-%d hours", hours))
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if rows.Scan(&id, &n) == nil {
			out[id] = n
		}
	}
	jsonOK(w, out)
}
