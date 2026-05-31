package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/kristianwind/yggdrasil/internal/docker"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type serverRow struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	GameskillID   string `json:"gameskill_id"`
	RealmID       string `json:"realm_id,omitempty"`
	Status        string `json:"status"`
	ContainerID   string `json:"container_id,omitempty"`
	EnvJSON       string `json:"-"`
	PortsJSON     string `json:"-"`
	DataDir       string `json:"data_dir"`
	Installed     bool   `json:"installed"`
	InstallStatus string `json:"install_status"`
	CreatedAt     string `json:"created_at"`
}

const serverCols = "id, name, gameskill_id, COALESCE(realm_id,''), status, COALESCE(container_id,''), data_dir, installed, install_status, created_at"

func scanServer(sc interface{ Scan(...any) error }) (serverRow, error) {
	var srv serverRow
	var installed int
	err := sc.Scan(&srv.ID, &srv.Name, &srv.GameskillID, &srv.RealmID,
		&srv.Status, &srv.ContainerID, &srv.DataDir, &installed, &srv.InstallStatus, &srv.CreatedAt)
	srv.Installed = installed == 1
	return srv, err
}

func (srv serverRow) target() rbac.Target {
	return rbac.Target{ServerID: srv.ID, RealmID: srv.RealmID, GameskillID: srv.GameskillID}
}

func (s *Server) handleListServers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT "+serverCols+" FROM servers ORDER BY name")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var list []serverRow
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			continue
		}
		// Non-admins only see servers they have view access to.
		if !s.allowed(r, rbac.ServerView, srv.target()) {
			continue
		}
		list = append(list, srv)
	}
	if list == nil {
		list = []serverRow{}
	}
	jsonOK(w, list)
}

func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerView, srv.target()) {
		return
	}
	jsonOK(w, srv)
}

func (s *Server) getServer(ctx context.Context, id string) (*serverRow, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+serverCols+" FROM servers WHERE id=?", id)
	srv, err := scanServer(row)
	return &srv, err
}

func (s *Server) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string            `json:"name"`
		GameskillID string            `json:"gameskill_id"`
		RealmID     string            `json:"realm_id"`
		Env         map[string]string `json:"env"`
		CPUPercent  float64           `json:"cpu_percent"`
		MemoryMB    int64             `json:"memory_mb"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.GameskillID == "" {
		jsonError(w, "name and gameskill_id required", http.StatusBadRequest)
		return
	}
	if !s.can(w, r, rbac.ServerCreate, rbac.Target{RealmID: req.RealmID, GameskillID: req.GameskillID}) {
		return
	}

	// Load gameskill
	var yamlBlob string
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT yaml_blob FROM gameskills WHERE id=?", req.GameskillID).Scan(&yamlBlob); err != nil {
		jsonError(w, "gameskill not found", http.StatusBadRequest)
		return
	}
	gs, err := gameskill.Parse([]byte(yamlBlob))
	if err != nil {
		jsonError(w, "gameskill parse error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Merge defaults with user-provided env
	env := gameskill.DefaultEnv(gs)
	for k, v := range req.Env {
		env[k] = v
	}

	// Allocate ports
	allocatedPorts := map[string]int{}
	for _, p := range gs.Ports {
		hostPort, err := s.allocatePort(r.Context(), p.Default)
		if err != nil {
			jsonError(w, "port allocation failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		allocatedPorts[p.Name] = hostPort
	}

	// Create data directory
	serverID := uuid.New().String()
	dataDir := filepath.Join(s.cfg.Database.Path[:len(s.cfg.Database.Path)-len(filepath.Base(s.cfg.Database.Path))], "servers", serverID)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		jsonError(w, "create data dir: "+err.Error(), http.StatusInternalServerError)
		return
	}

	envJSON, _ := json.Marshal(env)
	portsJSON, _ := json.Marshal(allocatedPorts)
	realmID := req.RealmID
	if realmID == "" {
		realmID = s.ensureRealm(r.Context(), gs.Category)
	}

	_, err = s.db.ExecContext(r.Context(), `
		INSERT INTO servers (id, name, gameskill_id, realm_id, status, env_json, ports_json, cpu_limit, mem_limit_mb, data_dir)
		VALUES (?,?,?,?,?,?,?,?,?,?)
	`, serverID, req.Name, req.GameskillID, nullableStr(realmID),
		"stopped", string(envJSON), string(portsJSON),
		req.CPUPercent, req.MemoryMB, dataDir)
	if err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Record port allocations
	for portName, hostPort := range allocatedPorts {
		proto := "tcp"
		for _, p := range gs.Ports {
			if p.Name == portName {
				proto = p.Protocol
			}
		}
		s.db.ExecContext(r.Context(),
			"INSERT INTO port_allocations (port, server_id, protocol, name) VALUES (?,?,?,?)",
			hostPort, serverID, proto, portName)
	}

	s.auditLog(r, "server.create", "server:"+serverID, map[string]string{"name": req.Name})

	// Kick off the install (download/build) immediately; progress streams over
	// the install/log WebSocket. The server can't start until this finishes.
	go s.runInstall(serverID) //nolint:errcheck

	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{"id": serverID, "status": "installing"})
}

func (s *Server) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerDelete, srv.target()) {
		return
	}
	if srv.ContainerID != "" {
		_ = s.docker.Remove(r.Context(), srv.ContainerID)
	}
	s.db.ExecContext(r.Context(), "DELETE FROM servers WHERE id=?", id)
	s.auditLog(r, "server.delete", "server:"+id, nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}

func (s *Server) handleStartServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}

	if !s.can(w, r, rbac.ServerControl, srv.target()) {
		return
	}

	// Gate on install completion.
	if s.install.isActive(id) {
		jsonError(w, "install in progress; please wait", http.StatusConflict)
		return
	}
	var installed int
	s.db.QueryRowContext(r.Context(), "SELECT installed FROM servers WHERE id=?", id).Scan(&installed)
	if installed == 0 {
		jsonError(w, "server is not installed yet; run install first", http.StatusConflict)
		return
	}

	// Load gameskill + the server's env and allocated host ports.
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "load runtime: "+err.Error(), http.StatusInternalServerError)
		return
	}
	gs, env, ports := rt.gs, rt.env, rt.ports

	// Build docker env slice (server vars + PORT_<name> helpers).
	envSlice := []string{}
	for k, v := range env {
		envSlice = append(envSlice, fmt.Sprintf("%s=%s", k, v))
	}
	for name, port := range ports {
		envSlice = append(envSlice, fmt.Sprintf("PORT_%s=%d", name, port))
	}

	// Build port mappings.
	portMappings := []docker.PortMapping{}
	for _, p := range gs.Ports {
		if hostPort, ok := ports[p.Name]; ok {
			portMappings = append(portMappings, docker.PortMapping{
				HostPort:      hostPort,
				ContainerPort: p.Default,
				Protocol:      p.Protocol,
			})
		}
	}

	image := gameskill.ApplyTemplate(gs.Docker.Image, env)
	// The startup command from the gameskill becomes the container command,
	// run via a shell so templated flags and env expansion work.
	startupCmd := gameskill.ApplyTemplate(gs.Startup.Command, env)
	cmd := []string{"/bin/sh", "-lc", startupCmd}
	containerName := fmt.Sprintf("ygg-%s", id[:8])

	// Per-server resource caps.
	var cpuLimit float64
	var memLimit int64
	s.db.QueryRowContext(r.Context(),
		"SELECT COALESCE(cpu_limit,0), COALESCE(mem_limit_mb,0) FROM servers WHERE id=?", id).
		Scan(&cpuLimit, &memLimit)

	// If container already exists, start it
	if srv.ContainerID != "" {
		if err := s.docker.Start(r.Context(), srv.ContainerID); err == nil {
			s.db.ExecContext(r.Context(), "UPDATE servers SET status='running' WHERE id=?", id)
			jsonOK(w, map[string]string{"status": "running"})
			return
		}
		// container gone — recreate
		s.docker.Remove(r.Context(), srv.ContainerID)
	}

	// Pull image
	s.docker.PullImage(r.Context(), image, io.Discard)

	containerID, err := s.docker.Create(r.Context(), docker.CreateOptions{
		Name:       containerName,
		Image:      image,
		Env:        envSlice,
		Cmd:        cmd,
		Ports:      portMappings,
		DataDir:    srv.DataDir,
		CPUPercent: cpuLimit,
		MemoryMB:   memLimit,
		Labels:     map[string]string{"yggdrasil.server_id": id},
	})
	if err != nil {
		jsonError(w, "create container: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.docker.Start(r.Context(), containerID); err != nil {
		jsonError(w, "start container: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.db.ExecContext(r.Context(),
		"UPDATE servers SET status='running', container_id=? WHERE id=?",
		containerID, id)

	s.auditLog(r, "server.start", "server:"+id, nil)
	jsonOK(w, map[string]string{"status": "running"})
}

func (s *Server) handleStopServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerControl, srv.target()) {
		return
	}
	if srv.ContainerID != "" {
		s.docker.Stop(r.Context(), srv.ContainerID, 30)
	}
	s.db.ExecContext(r.Context(), "UPDATE servers SET status='stopped' WHERE id=?", id)
	s.auditLog(r, "server.stop", "server:"+id, nil)
	jsonOK(w, map[string]string{"status": "stopped"})
}

func (s *Server) handleRestartServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerControl, srv.target()) {
		return
	}
	if srv.ContainerID != "" {
		s.docker.Restart(r.Context(), srv.ContainerID)
	}
	s.auditLog(r, "server.restart", "server:"+id, nil)
	jsonOK(w, map[string]string{"status": "restarting"})
}

func (s *Server) handleServerStats(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerView, srv.target()) {
		return
	}
	if srv.ContainerID == "" {
		jsonOK(w, map[string]interface{}{"cpu_percent": 0, "mem_mb": 0})
		return
	}
	stats, err := s.docker.GetStats(r.Context(), srv.ContainerID)
	if err != nil {
		jsonError(w, "stats error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, stats)
}

func (s *Server) handleServerLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerView, srv.target()) {
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	if srv.ContainerID == "" {
		conn.WriteMessage(websocket.TextMessage, []byte("[no container running]"))
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	rc, err := s.docker.Logs(ctx, srv.ContainerID, "200")
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("error: "+err.Error()))
		return
	}
	defer rc.Close()

	// Demux the multiplexed Docker stream into a pipe, then forward lines to WS.
	pr, pw := io.Pipe()
	go func() {
		_ = docker.DemuxCopy(pw, rc)
		pw.Close()
	}()
	go func() {
		<-ctx.Done()
		pr.Close()
	}()

	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if err := conn.WriteMessage(websocket.TextMessage, sc.Bytes()); err != nil {
			return
		}
	}
}

func (s *Server) handleConsole(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerConsole, srv.target()) {
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	if srv.ContainerID == "" {
		conn.WriteMessage(websocket.TextMessage, []byte("[server not running]"))
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	hijack, err := s.docker.Attach(ctx, srv.ContainerID)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("attach error: "+err.Error()))
		return
	}
	defer hijack.Close()

	// Docker output → WebSocket (demuxed line-by-line)
	go func() {
		pr, pw := io.Pipe()
		go func() {
			_ = docker.DemuxCopy(pw, hijack.Reader)
			pw.Close()
		}()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if err := conn.WriteMessage(websocket.TextMessage, sc.Bytes()); err != nil {
				cancel()
				return
			}
		}
		cancel()
	}()

	// WebSocket input → Docker stdin (one console command per message)
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		msg = append(msg, '\n')
		if _, err := hijack.Conn.Write(msg); err != nil {
			return
		}
	}
}

// Port allocation
func (s *Server) allocatePort(ctx context.Context, preferred int) (int, error) {
	// Check if preferred is free
	if preferred >= s.cfg.Ports.RangeMin && preferred <= s.cfg.Ports.RangeMax {
		var count int
		s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM port_allocations WHERE port=?", preferred).Scan(&count)
		if count == 0 {
			return preferred, nil
		}
	}
	// Find free port in range
	for port := s.cfg.Ports.RangeMin; port <= s.cfg.Ports.RangeMax; port++ {
		var count int
		s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM port_allocations WHERE port=?", port).Scan(&count)
		if count == 0 {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free ports in range %d-%d", s.cfg.Ports.RangeMin, s.cfg.Ports.RangeMax)
}

func (s *Server) ensureRealm(ctx context.Context, category string) string {
	if category == "" {
		category = "Default"
	}
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM realms WHERE name=?", category).Scan(&id)
	if err == nil {
		return id
	}
	id = uuid.New().String()
	s.db.ExecContext(ctx, "INSERT INTO realms (id, name) VALUES (?,?)", id, category)
	return id
}

func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
