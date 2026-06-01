package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	GameskillID   string            `json:"gameskill_id"`
	RealmID       string            `json:"realm_id,omitempty"`
	Status        string            `json:"status"`
	ContainerID   string            `json:"container_id,omitempty"`
	EnvJSON       string            `json:"-"`
	PortsJSON     string            `json:"-"`
	Ports         map[string]int    `json:"ports"`
	Env           map[string]string `json:"env,omitempty"` // populated only on single GET
	CPUPercent    float64           `json:"cpu_percent"`
	MemoryMB      int64             `json:"memory_mb"`
	DataDir       string            `json:"data_dir"`
	Installed     bool              `json:"installed"`
	InstallStatus string            `json:"install_status"`
	CreatedAt     string            `json:"created_at"`
}

const serverCols = "id, name, gameskill_id, COALESCE(realm_id,''), status, COALESCE(container_id,''), data_dir, installed, install_status, COALESCE(ports_json,'{}'), created_at"

func scanServer(sc interface{ Scan(...any) error }) (serverRow, error) {
	var srv serverRow
	var installed int
	err := sc.Scan(&srv.ID, &srv.Name, &srv.GameskillID, &srv.RealmID,
		&srv.Status, &srv.ContainerID, &srv.DataDir, &installed, &srv.InstallStatus, &srv.PortsJSON, &srv.CreatedAt)
	srv.Installed = installed == 1
	srv.Ports = map[string]int{}
	json.Unmarshal([]byte(srv.PortsJSON), &srv.Ports)
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
	// Scan every row and CLOSE the cursor before running any further queries.
	// modernc SQLite serves database/sql from a single connection, so issuing a
	// query (e.g. loadGrants) while this result set is still open deadlocks. This
	// only bit non-admins, because admins skip the grant lookup.
	var all []serverRow
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			continue
		}
		all = append(all, srv)
	}
	rows.Close()

	// Load the caller's grants once, then filter in memory.
	admin := isAdmin(r)
	var grants []rbac.Grant
	if !admin {
		if c := claimsFromContext(r.Context()); c != nil {
			grants = s.loadGrants(r.Context(), c.UserID)
		}
	}

	list := []serverRow{}
	for _, srv := range all {
		if admin || rbac.VisibleServer(grants, srv.target()) {
			list = append(list, srv)
		}
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
	// Single GET also returns the current variable values + resource caps so the
	// edit form can be pre-filled.
	var envJSON string
	s.db.QueryRowContext(r.Context(),
		"SELECT env_json, COALESCE(cpu_limit,0), COALESCE(mem_limit_mb,0) FROM servers WHERE id=?", id).
		Scan(&envJSON, &srv.CPUPercent, &srv.MemoryMB)
	srv.Env = map[string]string{}
	json.Unmarshal([]byte(envJSON), &srv.Env)
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

	// Allocate ports. Seed "taken" with host ports Docker already publishes (incl.
	// orphaned containers), track picks within this request, and test-bind each
	// candidate — so we never hand out a port that can't actually be bound.
	allocatedPorts := map[string]int{}
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
		allocatedPorts[p.Name] = hostPort
		taken[hostPort] = true
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

// handleUpdateServer edits an existing server's name, realm, variable values and
// resource caps. Variable changes (RAM, RCON password, seed, …) take effect on
// the next start; ones written into config files at install time (RCON password,
// seed) require a reinstall to fully apply — the UI says so.
func (s *Server) handleUpdateServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerControl, srv.target()) {
		return
	}
	var req struct {
		Name       *string           `json:"name"`
		RealmID    *string           `json:"realm_id"`
		Env        map[string]string `json:"env"`
		CPUPercent *float64          `json:"cpu_percent"`
		MemoryMB   *int64            `json:"memory_mb"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Name != nil && *req.Name != "" {
		s.db.ExecContext(r.Context(), "UPDATE servers SET name=? WHERE id=?", *req.Name, id)
	}
	if req.RealmID != nil {
		s.db.ExecContext(r.Context(), "UPDATE servers SET realm_id=? WHERE id=?", nullableStr(*req.RealmID), id)
	}
	if req.Env != nil {
		// Merge onto the existing env so unspecified vars are preserved.
		current := map[string]string{}
		var envJSON string
		s.db.QueryRowContext(r.Context(), "SELECT env_json FROM servers WHERE id=?", id).Scan(&envJSON)
		json.Unmarshal([]byte(envJSON), &current)
		for k, v := range req.Env {
			current[k] = v
		}
		b, _ := json.Marshal(current)
		s.db.ExecContext(r.Context(), "UPDATE servers SET env_json=? WHERE id=?", string(b), id)
	}
	if req.CPUPercent != nil {
		s.db.ExecContext(r.Context(), "UPDATE servers SET cpu_limit=? WHERE id=?", *req.CPUPercent, id)
	}
	if req.MemoryMB != nil {
		s.db.ExecContext(r.Context(), "UPDATE servers SET mem_limit_mb=? WHERE id=?", *req.MemoryMB, id)
	}
	s.auditLog(r, "server.update", "server:"+id, nil)
	jsonOK(w, map[string]string{"status": "updated"})
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
	// Remove any UPnP mappings while the server row (and its ports) still exist.
	if s.upnpEnabled(r.Context()) {
		s.upnpRemoveServer(id)
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

	// Reconcile host ports: if a previously-allocated port is now held by another
	// container/process (e.g. an orphan from an earlier attempt), reallocate it so
	// the container can bind. This is what was running on the host that this
	// server itself wants — so we ignore ports our own (to-be-recreated) container
	// would hold; those are released when we remove it below.
	if srv.ContainerID != "" {
		s.docker.Stop(r.Context(), srv.ContainerID, 5)
		s.docker.Remove(r.Context(), srv.ContainerID)
		s.db.ExecContext(r.Context(), "UPDATE servers SET container_id='' WHERE id=?", id)
		srv.ContainerID = ""
	}
	reallocated := false
	dockerUsed, _ := s.docker.UsedHostPorts(r.Context()) // our container is already removed above
	taken := map[int]bool{}
	for _, hp := range ports {
		taken[hp] = true
	}
	for name, hp := range ports {
		if !dockerUsed[hp] && hostPortAvailable(hp) {
			continue
		}
		delete(taken, hp)
		newPort, aerr := s.allocatePort(r.Context(), hp, taken)
		if aerr != nil {
			jsonError(w, "port reconcile: "+aerr.Error(), http.StatusInternalServerError)
			return
		}
		ports[name] = newPort
		taken[newPort] = true
		s.db.ExecContext(r.Context(), "DELETE FROM port_allocations WHERE server_id=? AND name=?", id, name)
		s.db.ExecContext(r.Context(), "INSERT INTO port_allocations (port, server_id, name) VALUES (?,?,?)", newPort, id, name)
		reallocated = true
	}
	if reallocated {
		portsJSON, _ := json.Marshal(ports)
		s.db.ExecContext(r.Context(), "UPDATE servers SET ports_json=? WHERE id=?", string(portsJSON), id)
	}

	// Build docker env slice (server vars + PORT_<name> helpers). HOME=/data gives
	// the (non-root) runtime user a writable home for caches.
	envSlice := []string{"HOME=/data"}
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
	// Use -c (not -lc) so a single-command startup is exec'd as PID 1 — that way
	// `docker stop`/SIGTERM reaches the game directly and it shuts down cleanly.
	cmd := []string{"/bin/sh", "-c", startupCmd}
	containerName := fmt.Sprintf("ygg-%s", id[:8])

	// Per-server resource caps.
	var cpuLimit float64
	var memLimit int64
	s.db.QueryRowContext(r.Context(),
		"SELECT COALESCE(cpu_limit,0), COALESCE(mem_limit_mb,0) FROM servers WHERE id=?", id).
		Scan(&cpuLimit, &memLimit)

	// Remove any orphaned container with our deterministic name so Create can't
	// fail with a name conflict (the container is always (re)created fresh; game
	// state lives in the /data volume).
	s.docker.Remove(r.Context(), containerName)

	// Pull image
	s.docker.PullImage(r.Context(), image, io.Discard)

	containerID, err := s.docker.Create(r.Context(), docker.CreateOptions{
		Name:  containerName,
		Image: image,
		Env:   envSlice,
		Cmd:   cmd,
		// Run as the panel's user so files the game writes (server.properties,
		// world, …) stay editable from the file manager. Install runs as root and
		// chowns /data to this uid afterwards.
		User:       fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
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

	// Mark "starting" while the game boots; a watcher promotes it to "running"
	// once the gameskill's done_regex appears (or the container stays up).
	s.db.ExecContext(r.Context(),
		"UPDATE servers SET status='starting', container_id=? WHERE id=?",
		containerID, id)
	var doneRegex string
	if rt, err := s.loadRuntime(r.Context(), id); err == nil {
		doneRegex = rt.gs.Startup.DoneRegex
	}
	go s.watchStartupReady(id, containerID, doneRegex)
	go s.upnpAddServer(id, srv.Name)

	s.auditLog(r, "server.start", "server:"+id, nil)
	s.notifyAll("▶️ " + srv.Name + " started")
	jsonOK(w, map[string]string{"status": "starting"})
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
		// Graceful shutdown: send the gameskill's stop console command first
		// (e.g. Minecraft "stop"), so the world saves before the container stops.
		if rt, err := s.loadRuntime(r.Context(), id); err == nil && rt.gs.Startup.Stop != "" {
			s.docker.SendStdin(r.Context(), srv.ContainerID, rt.gs.Startup.Stop)
		}
		if err := s.docker.Stop(r.Context(), srv.ContainerID, 30); err != nil {
			jsonError(w, "stop failed: "+err.Error(), http.StatusBadGateway)
			return
		}
	}
	s.db.ExecContext(r.Context(), "UPDATE servers SET status='stopped' WHERE id=?", id)
	go s.upnpRemoveServer(id)
	s.auditLog(r, "server.stop", "server:"+id, nil)
	s.notifyAll("⏹️ " + srv.Name + " stopped")
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
	if srv.ContainerID == "" {
		jsonError(w, "server is not running", http.StatusConflict)
		return
	}
	if err := s.docker.Restart(r.Context(), srv.ContainerID); err != nil {
		jsonError(w, "restart failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.db.ExecContext(r.Context(), "UPDATE servers SET status='starting' WHERE id=?", id)
	var doneRegex string
	if rt, err := s.loadRuntime(r.Context(), id); err == nil {
		doneRegex = rt.gs.Startup.DoneRegex
	}
	go s.watchStartupReady(id, srv.ContainerID, doneRegex)
	s.auditLog(r, "server.restart", "server:"+id, nil)
	jsonOK(w, map[string]string{"status": "starting"})
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
	kaStop := make(chan struct{})
	defer close(kaStop)
	go wsKeepalive(conn, kaStop)

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
	kaStop := make(chan struct{})
	defer close(kaStop)
	go wsKeepalive(conn, kaStop)

	if srv.ContainerID == "" {
		conn.WriteMessage(websocket.TextMessage, []byte("[server is not running — press Start to launch it]"))
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Don't attach to a container that isn't running (Docker returns 409). If the
	// DB still says "running", reconcile it to "stopped". Show the container's
	// last output so a crash-on-start (e.g. a bad mod, a missing jar) is visible
	// instead of a blank "press Start".
	if running, _, _ := s.docker.State(ctx, srv.ContainerID); !running {
		s.db.ExecContext(r.Context(), "UPDATE servers SET status='stopped' WHERE id=?", id)
		conn.WriteMessage(websocket.TextMessage, []byte("[server is not running — showing its last output below]"))
		s.showRecentLogs(ctx, conn, srv.ContainerID)
		conn.WriteMessage(websocket.TextMessage, []byte("[end of output — press Start to launch it again]"))
		return
	}

	hijack, err := s.docker.Attach(ctx, srv.ContainerID)
	if err != nil {
		// Most often a 409: the container exited between the running-check and the
		// attach (it crashed right after start). Surface its logs so the user can
		// see why, rather than a cryptic "received 409".
		s.db.ExecContext(r.Context(), "UPDATE servers SET status='stopped' WHERE id=?", id)
		conn.WriteMessage(websocket.TextMessage, []byte("[could not attach — the server exited right after starting. Last output:]"))
		s.showRecentLogs(ctx, conn, srv.ContainerID)
		conn.WriteMessage(websocket.TextMessage, []byte("[end of output — fix the cause above, then press Start]"))
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

// showRecentLogs streams a container's recent output to the console WebSocket.
// Used when we can't attach to a live container (it never started or crashed
// on start) so the user sees the actual failure instead of a 409.
func (s *Server) showRecentLogs(ctx context.Context, conn *websocket.Conn, containerID string) {
	rc, err := s.docker.Logs(ctx, containerID, "300")
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("(no logs available: "+err.Error()+")"))
		return
	}
	defer rc.Close()
	pr, pw := io.Pipe()
	go func() {
		_ = docker.DemuxCopy(pw, rc)
		pw.Close()
	}()
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if conn.WriteMessage(websocket.TextMessage, sc.Bytes()) != nil {
			return
		}
	}
}

// Port allocation
// allocatePort returns a free host port, preferring `preferred`. A port is free
// only if it is not in the port_allocations table, not in `taken` (already
// chosen earlier in this request), and not actually bound on the host.
func (s *Server) allocatePort(ctx context.Context, preferred int, taken map[int]bool) (int, error) {
	free := func(port int) bool {
		if taken[port] {
			return false
		}
		var count int
		s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM port_allocations WHERE port=?", port).Scan(&count)
		if count != 0 {
			return false
		}
		return hostPortAvailable(port)
	}
	if preferred >= s.cfg.Ports.RangeMin && preferred <= s.cfg.Ports.RangeMax && free(preferred) {
		return preferred, nil
	}
	for port := s.cfg.Ports.RangeMin; port <= s.cfg.Ports.RangeMax; port++ {
		if free(port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free ports in range %d-%d", s.cfg.Ports.RangeMin, s.cfg.Ports.RangeMax)
}

// hostPortAvailable reports whether a TCP host port can be bound right now —
// catching ports held by other containers/processes that aren't in our table.
func hostPortAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
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
