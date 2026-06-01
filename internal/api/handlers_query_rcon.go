package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
	"github.com/kristianwind/yggdrasil/internal/query"
	"github.com/kristianwind/yggdrasil/internal/rbac"
	"github.com/kristianwind/yggdrasil/internal/rcon"
)

// serverRuntime bundles the parsed gameskill plus a server's env and host ports.
type serverRuntime struct {
	gs    *gameskill.Gameskill
	env   map[string]string
	ports map[string]int
}

func (s *Server) loadRuntime(ctx context.Context, serverID string) (*serverRuntime, error) {
	var gameskillID, envJSON, portsJSON, name string
	err := s.db.QueryRowContext(ctx,
		"SELECT gameskill_id, env_json, ports_json, name FROM servers WHERE id=?", serverID).
		Scan(&gameskillID, &envJSON, &portsJSON, &name)
	if err != nil {
		return nil, err
	}
	var yamlBlob string
	if err := s.db.QueryRowContext(ctx,
		"SELECT yaml_blob FROM gameskills WHERE id=?", gameskillID).Scan(&yamlBlob); err != nil {
		return nil, err
	}
	gs, err := gameskill.Parse([]byte(yamlBlob))
	if err != nil {
		return nil, err
	}
	rt := &serverRuntime{gs: gs, env: map[string]string{}, ports: map[string]int{}}
	json.Unmarshal([]byte(envJSON), &rt.env)
	json.Unmarshal([]byte(portsJSON), &rt.ports)
	// Inject the panel's server name so gameskills can use {{SERVER_NAME}} as the
	// in-game/browser name without a duplicate form field.
	rt.env["SERVER_NAME"] = name
	// Expose each allocated host port as <NAME>_PORT (e.g. GAME_PORT, QUERY_PORT)
	// so gameskills bind/advertise the actual external port. For Steam games this
	// is essential: the server registers its bind port with the Steam master, so
	// it must equal the forwarded port (the container also publishes these 1:1).
	for portName, hostPort := range rt.ports {
		rt.env[strings.ToUpper(portName)+"_PORT"] = strconv.Itoa(hostPort)
	}
	return rt, nil
}

// queryPort returns the best host port to query: a "query" mapping if present,
// else the "game" mapping.
func (rt *serverRuntime) queryPort() int {
	if p, ok := rt.ports["query"]; ok {
		return p
	}
	return rt.ports["game"]
}

func (s *Server) handleServerQuery(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerView, s.serverTarget(r.Context(), id)) {
		return
	}
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if rt.gs.Query == nil {
		jsonOK(w, map[string]any{"online": false, "supported": false})
		return
	}
	st, err := query.Query(rt.gs.Query.Type, "127.0.0.1", rt.queryPort(), 3*time.Second)
	if err != nil {
		// A query failure usually just means the server isn't up yet.
		jsonOK(w, map[string]any{"online": false, "supported": true})
		return
	}
	jsonOK(w, st)
}

func (s *Server) handleServerRcon(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Command string `json:"command"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Command == "" {
		jsonError(w, "command required", http.StatusBadRequest)
		return
	}
	if !s.can(w, r, rbac.ServerConsole, s.serverTarget(r.Context(), id)) {
		return
	}
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if rt.gs.RCON == nil || !rt.gs.RCON.Enabled {
		jsonError(w, "this game has no RCON; use the console instead", http.StatusBadRequest)
		return
	}

	port := rt.ports["rcon"]
	if port == 0 {
		port = rt.ports["game"] // BattlEye shares the game port
	}
	password := ""
	if rt.gs.RCON.PasswordVar != "" {
		password = rt.env[rt.gs.RCON.PasswordVar]
	}

	client, err := rcon.Dial(rcon.Config{
		Type:     rt.gs.RCON.Type,
		Host:     "127.0.0.1",
		Port:     port,
		Password: password,
		Timeout:  5 * time.Second,
	})
	if err != nil {
		jsonError(w, "rcon connect: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer client.Close()

	out, err := client.Execute(req.Command)
	if err != nil {
		jsonError(w, "rcon exec: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.auditLog(r, "server.rcon", "server:"+id, map[string]string{"command": req.Command})
	jsonOK(w, map[string]string{"response": out})
}
