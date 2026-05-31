package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Centralized ban management is a global-admin feature: a cross-server ban list
// that can ban a player on one server or everywhere at once, with reason + audit.

type banView struct {
	ID         string `json:"id"`
	PlayerName string `json:"player_name"`
	PlayerID   string `json:"player_id,omitempty"`
	ServerID   string `json:"server_id,omitempty"`   // empty = all servers (global)
	ServerName string `json:"server_name,omitempty"` // "All servers" when global
	Reason     string `json:"reason,omitempty"`
	BannedBy   string `json:"banned_by,omitempty"`
	CreatedAt  string `json:"created_at"`
}

func (s *Server) handleListBans(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT b.id, b.player_name, COALESCE(b.player_id,''), COALESCE(b.server_id,''),
		       COALESCE(s.name,''), COALESCE(b.reason,''), COALESCE(u.username,''), b.created_at
		FROM bans b
		LEFT JOIN servers s ON s.id = b.server_id
		LEFT JOIN users u ON u.id = b.banned_by
		ORDER BY b.created_at DESC`)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := []banView{}
	for rows.Next() {
		var b banView
		if err := rows.Scan(&b.ID, &b.PlayerName, &b.PlayerID, &b.ServerID,
			&b.ServerName, &b.Reason, &b.BannedBy, &b.CreatedAt); err != nil {
			continue
		}
		if b.ServerID == "" {
			b.ServerName = "All servers"
		}
		list = append(list, b)
	}
	jsonOK(w, list)
}

func (s *Server) handleCreateBan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PlayerName string `json:"player_name"`
		PlayerID   string `json:"player_id"`
		ServerID   string `json:"server_id"` // empty = all servers
		Reason     string `json:"reason"`
	}
	if err := decodeJSON(r, &req); err != nil || req.PlayerName == "" {
		jsonError(w, "player_name required", http.StatusBadRequest)
		return
	}
	claims := claimsFromContext(r.Context())
	banID := uuid.New().String()
	_, err := s.db.ExecContext(r.Context(),
		"INSERT INTO bans (id, player_name, player_id, server_id, reason, banned_by) VALUES (?,?,?,?,?,?)",
		banID, req.PlayerName, nullableStr(req.PlayerID), nullableStr(req.ServerID),
		nullableStr(req.Reason), claims.UserID)
	if err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Push the ban to the scoped server(s) that support a console ban.
	pushed := 0
	for _, sid := range s.scopeServers(req.ServerID, "") {
		if s.pushBan(r.Context(), sid, req.PlayerName, req.Reason, true) {
			pushed++
		}
	}
	s.auditLog(r, "ban.create", "player:"+req.PlayerName,
		map[string]any{"server_id": req.ServerID, "pushed": pushed})
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]any{"id": banID, "pushed": pushed})
}

func (s *Server) handleDeleteBan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var playerName, serverID string
	err := s.db.QueryRowContext(r.Context(),
		"SELECT player_name, COALESCE(server_id,'') FROM bans WHERE id=?", id).
		Scan(&playerName, &serverID)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	s.db.ExecContext(r.Context(), "DELETE FROM bans WHERE id=?", id)

	pushed := 0
	for _, sid := range s.scopeServers(serverID, "") {
		if s.pushBan(r.Context(), sid, playerName, "", false) {
			pushed++
		}
	}
	s.auditLog(r, "ban.delete", "player:"+playerName, map[string]int{"pushed": pushed})
	jsonOK(w, map[string]any{"status": "unbanned", "pushed": pushed})
}

// pushBan issues the (un)ban console command for a server's gameskill, if it
// declares one. Returns true if a command was sent.
func (s *Server) pushBan(ctx context.Context, serverID, player, reason string, ban bool) bool {
	rt, err := s.loadRuntime(ctx, serverID)
	if err != nil || rt.gs.Bans == nil {
		return false
	}
	tmpl := rt.gs.Bans.UnbanCommand
	if ban {
		tmpl = rt.gs.Bans.BanCommand
	}
	if tmpl == "" {
		return false
	}
	cmd := strings.ReplaceAll(tmpl, "{{player}}", player)
	cmd = strings.ReplaceAll(cmd, "{{reason}}", reason)
	s.sendToServer(serverID, cmd)
	return true
}
