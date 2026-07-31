package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// Seed the rune's variable defaults first, then overlay the server's stored
	// values. Without this, a server created before the rune added a variable has
	// no value for it, so a template like "{{ONLINE_MODE}}" in docker.env passes
	// through literally and the container rejects it (this broke Bedrock servers
	// when the rune gained ONLINE_MODE). Defaults make new variables safe for
	// existing servers; env_json still wins wherever it has a value.
	rt := &serverRuntime{gs: gs, env: gameskill.DefaultEnv(gs), ports: map[string]int{}}
	json.Unmarshal([]byte(envJSON), &rt.env)
	// Decrypt secret-typed env (RCON password, password vars) — this is the one
	// path that feeds real values to the container and RCON.
	s.decryptSecretEnv(rt.env, gs)
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
	// The address this server answers on from outside. Plenty of apps have to be
	// told their own public URL — WordPress stores it, Immich, Gitea and n8n build
	// links and OAuth redirects from it — and none of them can be given a working
	// default in a rune, because the port isn't chosen until the server is
	// created (allocatePort ignores the rune's preferred port on purpose). So the
	// panel, which is the only thing that knows the answer, supplies it.
	rt.env["PUBLIC_URL"] = s.publicURL(ctx, serverID, rt.ports)
	// Values the admin typed can reference it too, so a variable like a site URL
	// can be set to "{{PUBLIC_URL}}" in the form instead of being retyped whenever
	// the address changes. Only built-ins are expanded here, and only one level:
	// this is a convenience, not a template language.
	for k, v := range rt.env {
		if k != "PUBLIC_URL" && strings.Contains(v, "{{PUBLIC_URL}}") {
			rt.env[k] = strings.ReplaceAll(v, "{{PUBLIC_URL}}", rt.env["PUBLIC_URL"])
		}
	}
	return rt, nil
}

// publicURL is the externally reachable base URL for a server: its own domain
// when one is configured, else the panel host and the server's allocated port.
// Empty when neither is known, so a caller can tell "no answer" from a guess.
func (s *Server) publicURL(ctx context.Context, serverID string, ports map[string]int) string {
	// A configured domain wins: it is what a visitor actually types, and it is
	// already how the panel decides a server's hostname elsewhere (see
	// serverBlockHost, which this deliberately reuses so the two never diverge).
	if host := s.serverBlockHost(serverID); host != "" {
		return "https://" + host
	}
	host := firstNonEmpty(s.getSetting(ctx, "public_hostname"), s.detectPublicAddr())
	if host == "" {
		return ""
	}
	port := ports["web"]
	if port == 0 {
		port = ports["game"]
	}
	if port == 0 {
		for _, p := range ports { // any allocated port beats none
			port = p
			break
		}
	}
	if port == 0 {
		return "http://" + host
	}
	return fmt.Sprintf("http://%s:%d", host, port)
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

// rconExec dials a server's RCON, runs one command, and returns the response.
// Shared by the raw console endpoint, the players tab, and any other RCON caller
// that needs the reply text (sendToServer is the fire-and-forget cousin). Returns
// errNoRCON when the rune has no enabled rcon: block so callers can map it to 400.
func (s *Server) rconExec(ctx context.Context, serverID, command string) (string, error) {
	rt, err := s.loadRuntime(ctx, serverID)
	if err != nil {
		return "", err
	}
	if rt.gs.RCON == nil || !rt.gs.RCON.Enabled {
		return "", errNoRCON
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
		return "", fmt.Errorf("rcon connect: %w", err)
	}
	defer client.Close()
	return client.Execute(command)
}

// errNoRCON marks a rune without an enabled rcon: block.
var errNoRCON = errors.New("this game has no RCON; use the console instead")

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
	out, err := s.rconExec(r.Context(), id, req.Command)
	if err != nil {
		if errors.Is(err, errNoRCON) {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonError(w, "rcon: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.auditLog(r, "server.rcon", "server:"+id, map[string]string{"command": req.Command})
	jsonOK(w, map[string]string{"response": out})
}
