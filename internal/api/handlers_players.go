package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// Players tab: a live, rune-declared roster over RCON plus kick / broadcast /
// lock. Everything the rune declares (list command + per-line parse regex, and
// which action commands exist) drives a generic UI, so DayZ is just the first
// game to fill the fields — the same tab lights up for any rune with a players:
// block. These are the deterministic moderation "hands"; the AI layer can later
// drive the very same endpoints.
//
// All of it flows through RCON, so it's gated on ServerConsole — anyone who can
// kick could already do so with a raw RCON command; this is just the friendly
// front end, not a new capability.

type playerInfo struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	Ping string `json:"ping,omitempty"`
	GUID string `json:"guid,omitempty"`
	IP   string `json:"ip,omitempty"`
}

// parsePlayers applies a rune's per-line player_regex to an RCON list response
// and returns one entry per matching line, mapping named capture groups
// (name/id/ping/guid/ip) onto the struct. Lines that don't match (headers,
// separators, totals) are skipped.
func parsePlayers(output, pattern string) ([]playerInfo, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	names := re.SubexpNames()
	players := []playerInfo{}
	for _, line := range strings.Split(output, "\n") {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var p playerInfo
		for i, name := range names {
			if i == 0 || name == "" || i >= len(m) {
				continue
			}
			v := strings.TrimSpace(m[i])
			switch name {
			case "id":
				p.ID = v
			case "name":
				p.Name = v
			case "ping":
				p.Ping = v
			case "guid":
				p.GUID = v
			case "ip":
				p.IP = v
			}
		}
		if p.Name != "" || p.ID != "" {
			players = append(players, p)
		}
	}
	return players, nil
}

// templatePlayerCmd fills {{id}}/{{name}}/{{reason}}/{{message}} in a command
// template, sanitizing each value so a crafted player name / reason can't inject
// a second console line (same defense as ban commands).
func templatePlayerCmd(tmpl string, vars map[string]string) string {
	out := tmpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", sanitizeConsoleArg(v))
	}
	return out
}

type playersResponse struct {
	Supported    bool         `json:"supported"`
	Online       bool         `json:"online"`
	Players      []playerInfo `json:"players"`
	CanKick      bool         `json:"can_kick"`
	CanBroadcast bool         `json:"can_broadcast"`
	CanLock      bool         `json:"can_lock"`
}

func (s *Server) handleListPlayers(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerConsole, s.serverTarget(r.Context(), id)) {
		return
	}
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	pl := rt.gs.Players
	if pl == nil {
		jsonOK(w, playersResponse{Supported: false, Players: []playerInfo{}})
		return
	}
	resp := playersResponse{
		Supported:    true,
		Players:      []playerInfo{},
		CanKick:      pl.KickCommand != "",
		CanBroadcast: pl.BroadcastCommand != "",
		CanLock:      pl.LockCommand != "",
	}
	out, err := s.rconExec(r.Context(), id, pl.ListCommand)
	if err != nil {
		// RCON unreachable almost always just means the server is down/starting;
		// report offline rather than erroring so the tab renders cleanly.
		jsonOK(w, resp)
		return
	}
	resp.Online = true
	if players, perr := parsePlayers(out, pl.PlayerRegex); perr == nil {
		resp.Players = players
	}
	jsonOK(w, resp)
}

func (s *Server) handleKickPlayer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerConsole, s.serverTarget(r.Context(), id)) {
		return
	}
	var req struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}
	decodeJSON(r, &req)
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if rt.gs.Players == nil || rt.gs.Players.KickCommand == "" {
		jsonError(w, "this game does not support kicking", http.StatusBadRequest)
		return
	}
	if req.ID == "" && req.Name == "" {
		jsonError(w, "player id or name required", http.StatusBadRequest)
		return
	}
	cmd := templatePlayerCmd(rt.gs.Players.KickCommand, map[string]string{
		"id": req.ID, "name": req.Name, "reason": req.Reason,
	})
	if err := s.rconCommand(w, r, id, cmd); err != nil {
		return
	}
	s.auditLog(r, "player.kick", "server:"+id, map[string]string{"player": req.Name + req.ID, "reason": req.Reason})
	jsonOK(w, map[string]string{"status": "kicked"})
}

func (s *Server) handleBroadcast(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerConsole, s.serverTarget(r.Context(), id)) {
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Message) == "" {
		jsonError(w, "message required", http.StatusBadRequest)
		return
	}
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if rt.gs.Players == nil || rt.gs.Players.BroadcastCommand == "" {
		jsonError(w, "this game does not support broadcast", http.StatusBadRequest)
		return
	}
	cmd := templatePlayerCmd(rt.gs.Players.BroadcastCommand, map[string]string{"message": req.Message})
	if err := s.rconCommand(w, r, id, cmd); err != nil {
		return
	}
	s.auditLog(r, "player.broadcast", "server:"+id, map[string]string{"message": req.Message})
	jsonOK(w, map[string]string{"status": "sent"})
}

func (s *Server) handleLockServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerConsole, s.serverTarget(r.Context(), id)) {
		return
	}
	var req struct {
		Locked bool `json:"locked"`
	}
	decodeJSON(r, &req)
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if rt.gs.Players == nil {
		jsonError(w, "not supported", http.StatusBadRequest)
		return
	}
	cmd := rt.gs.Players.LockCommand
	if !req.Locked {
		cmd = rt.gs.Players.UnlockCommand
	}
	if cmd == "" {
		jsonError(w, "this game does not support lock/unlock", http.StatusBadRequest)
		return
	}
	if err := s.rconCommand(w, r, id, cmd); err != nil {
		return
	}
	verb := "locked"
	if !req.Locked {
		verb = "unlocked"
	}
	s.auditLog(r, "server.lock", "server:"+id, map[string]bool{"locked": req.Locked})
	jsonOK(w, map[string]string{"status": verb})
}

// rconCommand runs a command over RCON and writes an HTTP error itself on
// failure (returning non-nil so the handler can just `return`). Success is
// silent. Centralizes the errNoRCON→400 / connect-fail→502 mapping shared by the
// player-action handlers.
func (s *Server) rconCommand(w http.ResponseWriter, r *http.Request, serverID, command string) error {
	_, err := s.rconExec(r.Context(), serverID, command)
	if err != nil {
		if errors.Is(err, errNoRCON) {
			jsonError(w, err.Error(), http.StatusBadRequest)
		} else {
			jsonError(w, "rcon: "+err.Error(), http.StatusBadGateway)
		}
	}
	return err
}
