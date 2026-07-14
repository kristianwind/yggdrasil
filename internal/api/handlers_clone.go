package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// handleCloneServer duplicates a server's SETUP — same rune, variables, resource
// limits, host mounts and per-server toggles — into a brand-new server with freshly
// allocated ports and its own empty data directory, then installs it. It's the fast
// way to stand up "another one like this" (a second Minecraft world, a staging copy)
// without re-entering every setting.
//
// It clones configuration, NOT data: the new server does a fresh install of the
// rune rather than copying the source's (potentially multi-GB, live) game files.
// Identity/unique fields are deliberately not carried over — the clone gets its own
// ports, no subdomain, no BattleMetrics id, and is not shared on the status page.
func (s *Server) handleCloneServer(w http.ResponseWriter, r *http.Request) {
	srcID := chi.URLParam(r, "id")
	src, err := s.getServer(r.Context(), srcID)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	// Need to be able to see the source AND to create a server in its scope.
	if !s.can(w, r, rbac.ServerView, src.target()) {
		return
	}
	if !s.can(w, r, rbac.ServerCreate, rbac.Target{RealmID: src.RealmID, GameskillID: src.GameskillID}) {
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	decodeJSON(r, &req)

	// Pull the source's stored config directly (env_json is kept in its encrypted
	// form and copied verbatim, so secret values survive without re-encryption).
	var envJSON, hostMounts string
	var cpu float64
	var mem int64
	var autostart, autoForward, watchdog int
	err = s.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(env_json,'{}'), COALESCE(host_mounts,''), COALESCE(cpu_limit,0), COALESCE(mem_limit_mb,0),
		       COALESCE(autostart,1), COALESCE(auto_forward,1), COALESCE(watchdog,0)
		FROM servers WHERE id=?`, srcID).Scan(&envJSON, &hostMounts, &cpu, &mem, &autostart, &autoForward, &watchdog)
	if err != nil {
		jsonError(w, "source read failed", http.StatusInternalServerError)
		return
	}

	// Parse the rune to know which ports to allocate for the clone.
	var yamlBlob string
	if err := s.db.QueryRowContext(r.Context(), "SELECT yaml_blob FROM gameskills WHERE id=?", src.GameskillID).Scan(&yamlBlob); err != nil {
		jsonError(w, "gameskill not found", http.StatusBadRequest)
		return
	}
	gs, err := gameskill.Parse([]byte(yamlBlob))
	if err != nil {
		jsonError(w, "gameskill parse error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = src.Name + " (copy)"
	}

	// Allocate fresh ports (test-bound, avoiding ports Docker already publishes).
	allocated := map[string]int{}
	taken, _ := s.docker.UsedHostPorts(r.Context())
	if taken == nil {
		taken = map[int]bool{}
	}
	for _, p := range gs.Ports {
		hostPort, err := s.allocatePort(r.Context(), p.Default, taken)
		if err != nil {
			jsonError(w, "port allocation failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		allocated[p.Name] = hostPort
		taken[hostPort] = true
	}

	// Fresh, empty data directory.
	newID := uuid.New().String()
	base := s.cfg.Database.Path[:len(s.cfg.Database.Path)-len(filepath.Base(s.cfg.Database.Path))]
	dataDir := filepath.Join(base, "servers", newID)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		jsonError(w, "create data dir: "+err.Error(), http.StatusInternalServerError)
		return
	}

	portsJSON, _ := json.Marshal(allocated)
	if _, err := s.db.ExecContext(r.Context(), `
		INSERT INTO servers (id, name, gameskill_id, realm_id, status, env_json, ports_json, cpu_limit, mem_limit_mb,
		                     data_dir, host_mounts, autostart, auto_forward, watchdog)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		newID, name, src.GameskillID, nullableStr(src.RealmID), "stopped", envJSON, string(portsJSON), cpu, mem,
		dataDir, hostMounts, autostart, autoForward, watchdog); err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	for portName, hostPort := range allocated {
		proto := "tcp"
		for _, p := range gs.Ports {
			if p.Name == portName {
				proto = p.Protocol
			}
		}
		s.db.ExecContext(r.Context(),
			"INSERT INTO port_allocations (port, server_id, protocol, name) VALUES (?,?,?,?)",
			hostPort, newID, proto, portName)
	}

	s.auditLog(r, "server.clone", "server:"+newID, map[string]string{"name": name, "source": srcID})
	go s.runInstall(newID) //nolint:errcheck

	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{"id": newID, "status": "installing"})
}
