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

type kvasirEventDTO struct {
	TS          string `json:"ts"`
	Event       string `json:"event"`
	Detail      string `json:"detail"`
	Explanation string `json:"explanation"`
	Action      string `json:"action"`
	Args        string `json:"args"`
	Reason      string `json:"reason"`
	Level       int    `json:"level"`
	Applied     bool   `json:"applied"`
	ApplyStatus string `json:"apply_status"`
}

// handleServerKvasirEvents returns a server's recent proactive-AI (Kvasir)
// reactions — what it saw, explained and proposed or applied — over the last N
// hours (default 7 days, max 30), newest first. This is the in-panel surface so
// proposals aren't only visible in Discord.
func (s *Server) handleServerKvasirEvents(w http.ResponseWriter, r *http.Request) {
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
		`SELECT ts, event, detail, explanation, action, args, reason, level, applied, apply_status
		 FROM kvasir_events WHERE server_id=? AND ts >= datetime('now', ?) ORDER BY ts DESC LIMIT 50`,
		id, fmt.Sprintf("-%d hours", hours))
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := []kvasirEventDTO{}
	for rows.Next() {
		var e kvasirEventDTO
		var applied int
		if rows.Scan(&e.TS, &e.Event, &e.Detail, &e.Explanation, &e.Action, &e.Args, &e.Reason, &e.Level, &applied, &e.ApplyStatus) == nil {
			e.Applied = applied == 1
			list = append(list, e)
		}
	}
	jsonOK(w, list)
}

// handleClearServerKvasirEvents dismisses a server's Kvasir history once the admin
// has looked at it.
func (s *Server) handleClearServerKvasirEvents(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerControl, s.serverTarget(r.Context(), id)) {
		return
	}
	s.db.ExecContext(r.Context(), "DELETE FROM kvasir_events WHERE server_id=?", id)
	s.auditLog(r, "server.kvasir.clear", "server:"+id, nil)
	jsonOK(w, map[string]string{"status": "cleared"})
}

// handleClearServerCrashes clears a server's stability history — a dismiss for the
// warnings once the admin has looked at them.
func (s *Server) handleClearServerCrashes(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerControl, s.serverTarget(r.Context(), id)) {
		return
	}
	s.db.ExecContext(r.Context(), "DELETE FROM server_crashes WHERE server_id=?", id)
	s.auditLog(r, "server.crashes.clear", "server:"+id, nil)
	jsonOK(w, map[string]string{"status": "cleared"})
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
	// Count only genuine faults for the flapping badge — a graceful stop/restart
	// (exit 0/143/130) isn't a crash.
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT server_id, COUNT(*) FROM server_crashes WHERE ts >= datetime('now', ?) AND exit_code NOT IN (0,143,130) GROUP BY server_id",
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
