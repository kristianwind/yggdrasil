package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/query"
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
	Count        int          `json:"count"` // connected players — may exceed len(Players) when names aren't reported (DayZ over A2S)
	Players      []playerInfo `json:"players"`
	CanKick      bool         `json:"can_kick"`
	CanBroadcast bool         `json:"can_broadcast"`
	CanLock      bool         `json:"can_lock"`
	CanBan       bool         `json:"can_ban"` // DayZ: ban by writing ban.txt (effective on rejoin)
	Reason       string       `json:"reason,omitempty"` // why the list is unavailable (offline vs. RCON down)
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
		// RCON didn't answer. Rather than a dead tab, fall back to the query
		// protocol's own player list (A2S_PLAYER) when the rune exposes one — that
		// gives a read-only roster with no RCON at all. This is the reliable path
		// for DayZ, whose Linux server never answers BattlEye RCon regardless of
		// config. Kick / broadcast / lock still need RCON, so they stay disabled.
		if rt.gs.Query != nil {
			if names, qerr := query.QueryPlayers(rt.gs.Query.Type, "127.0.0.1", rt.queryPort(), 3*time.Second); qerr == nil {
				resp.Online = true
				resp.CanKick, resp.CanBroadcast, resp.CanLock = false, false, false
				// The A2S_PLAYER count is authoritative even when names come back blank.
				resp.Count = len(names)
				// DayZ's Linux server returns blank A2S names, but its admin log (.ADM)
				// records every join/leave with the real name + id. Replay it for a true
				// roster and enable banning (ban.txt, effective on rejoin).
				var dataDir string
				s.db.QueryRowContext(r.Context(), "SELECT data_dir FROM servers WHERE id=?", id).Scan(&dataDir)
				if roster := dayzADMRoster(dataDir); len(roster) > 0 {
					for _, p := range roster {
						resp.Players = append(resp.Players, playerInfo{Name: p.Name, ID: p.ID, GUID: p.ID})
					}
					resp.CanBan = true
					if len(roster) > resp.Count {
						resp.Count = len(roster)
					}
					resp.Reason = "Live roster from the DayZ admin log. Ban writes the player to ban.txt (effective on their next join — DayZ-Linux can't kick a player who's already in-game without RCon)."
				} else {
					// No admin-log names available — fall back to any non-blank A2S names.
					for _, n := range names {
						if n = strings.TrimSpace(n); n != "" {
							resp.Players = append(resp.Players, playerInfo{Name: n})
						}
					}
					if len(names) > len(resp.Players) {
						resp.Reason = "Player count from the Steam query port. This DayZ server isn't reporting player names right now (the admin log has no active session, and BattlEye RCon isn't reliable on Linux)."
					} else {
						resp.Reason = "Live roster from the Steam query port."
					}
				}
				jsonOK(w, resp)
				return
			}
		}
		// Distinguish "server is down" from "server is up but nothing answered", so
		// the tab gives an actionable message instead of a misleading "offline".
		if s.playersOnline(id) >= 0 {
			resp.Reason = "The server is up, but neither RCON nor the query port answered. If it just started, give it a moment and refresh."
		} else {
			resp.Reason = "Server is offline or still starting."
		}
		jsonOK(w, resp)
		return
	}
	resp.Online = true
	if players, perr := parsePlayers(out, pl.PlayerRegex); perr == nil {
		resp.Players = players
	}
	resp.Count = len(resp.Players)
	jsonOK(w, resp)
}

// handleDayzBan bans a DayZ player by adding their id to the server's ban.txt. DayZ
// reads it on connect, so the ban stops them rejoining; it can't remove a player
// already in-game (that needs RCon, which DayZ-Linux lacks). Same gate as kick.
func (s *Server) handleDayzBan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerConsole, s.serverTarget(r.Context(), id)) {
		return
	}
	var req struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}
	if decodeJSON(r, &req) != nil || strings.TrimSpace(req.ID) == "" {
		jsonError(w, "player id required", http.StatusBadRequest)
		return
	}
	var gameskillID, dataDir string
	if s.db.QueryRowContext(r.Context(), "SELECT gameskill_id, data_dir FROM servers WHERE id=?", id).Scan(&gameskillID, &dataDir) != nil || gameskillID != "dayz" {
		jsonError(w, "not a DayZ server", http.StatusBadRequest)
		return
	}
	added, err := dayzBanID(dataDir, strings.TrimSpace(req.ID))
	if err != nil {
		jsonError(w, "could not write ban.txt: "+err.Error(), http.StatusInternalServerError)
		return
	}
	name := req.Name
	if name == "" {
		name = req.ID
	}
	s.auditLog(r, "dayz.ban", "server:"+id, map[string]string{"id": req.ID, "name": req.Name, "reason": req.Reason})
	if added {
		go s.notifyServer(id, fmt.Sprintf("🔨 Banned %s — added to ban.txt, effective on their next join", name))
	}
	msg := "Ban added — it takes effect on their next join (a player already in-game can't be kicked without RCon)."
	if !added {
		msg = "That player is already in ban.txt."
	}
	jsonOK(w, map[string]any{"banned": req.ID, "added": added, "message": msg})
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
