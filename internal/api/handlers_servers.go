package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/kristianwind/yggdrasil/internal/docker"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

var wsUpgrader = websocket.Upgrader{
	// Reject cross-site WebSocket handshakes (the console WS writes to the container
	// stdin). Same-origin browser connections and non-browser clients (no Origin,
	// e.g. CLI/automation) are allowed.
	CheckOrigin: func(r *http.Request) bool {
		o := r.Header.Get("Origin")
		return o == "" || sameOriginHost(o, r.Host)
	},
}

type serverRow struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	GameskillID    string            `json:"gameskill_id"`
	RealmID        string            `json:"realm_id,omitempty"`
	Status         string            `json:"status"`
	ContainerID    string            `json:"container_id,omitempty"`
	EnvJSON        string            `json:"-"`
	PortsJSON      string            `json:"-"`
	Ports          map[string]int    `json:"ports"`
	Env            map[string]string `json:"env,omitempty"` // populated only on single GET
	Tags           []string          `json:"tags"`          // normalized labels for grouping/filtering
	CPUPercent     float64           `json:"cpu_percent"`
	MemoryMB       int64             `json:"memory_mb"`
	DataDir        string            `json:"data_dir"`
	Installed      bool              `json:"installed"`
	InstallStatus  string            `json:"install_status"`
	CreatedAt      string            `json:"created_at"`
	BMServerID     string            `json:"bm_server_id,omitempty"`
	AutoForward    bool              `json:"auto_forward"`
	Autostart      bool              `json:"autostart"`     // start on panel/host boot
	StatusPublic   bool              `json:"status_public"` // listed on the public /status page (opt-in)
	Subdomain      string            `json:"subdomain,omitempty"`
	Perms          []string          `json:"perms"`                  // caller's effective permissions on this server
	HostMountsJSON string            `json:"-"`                      // raw servers.host_mounts (admin host binds)
	HostMounts     []hostMount       `json:"host_mounts,omitempty"`  // populated on single GET for admins only
	WipeSupported  bool              `json:"wipe_supported"`         // rune declares a wipe: block (single GET)
	RestartWarn    bool              `json:"restart_warn"`           // rune declares restart warnings (single GET)
	Watchdog       bool              `json:"watchdog"`               // auto-heal enabled for this server
	WatchdogSup    bool              `json:"watchdog_supported"`     // rune has a query the watchdog can health-check (single GET)
	PlayersSup     bool              `json:"players_supported"`      // rune declares a players: block (Players tab; single GET)
	AdminLogSup    bool              `json:"admin_log_supported"`    // rune declares an admin_log: block (Activity tab; single GET)
	ModsSupported  bool              `json:"mods_supported"`         // SERVER_TYPE maps to a Modrinth loader (Mods tab; single GET)
	ConfigFiles    []string          `json:"config_files,omitempty"` // rune's config_files: the files worth editing (Files tab shortcuts; single GET)
	AIEnabled      bool              `json:"ai_enabled"`             // advisory AI features are on (digest button; single GET)
	CPUAlarmPct    int               `json:"cpu_alarm_pct"`          // alert when CPU% sustained at/above this (0 = off)
	MemAlarmMB     int               `json:"mem_alarm_mb"`           // alert when memory MB sustained at/above this (0 = off)
	DiskAlarmMB    int               `json:"disk_alarm_mb"`          // alert when the data dir grows to/above this many MB (0 = off)
	Notes          string            `json:"notes"`                  // free-text admin notes (single GET)
	NotesMarkdown  bool              `json:"notes_markdown"`         // render the note as markdown rather than plain text
	// NotesHTML is the note rendered server-side, present only when NotesMarkdown
	// is on. The frontend injects it, so it is produced by a renderer that drops
	// raw HTML and empties dangerous URLs — see notes_render.go. Never build this
	// anywhere else.
	NotesHTML string `json:"notes_html,omitempty"` // single GET
}

const serverCols = "id, name, gameskill_id, COALESCE(realm_id,''), status, COALESCE(container_id,''), data_dir, installed, install_status, COALESCE(ports_json,'{}'), created_at, COALESCE(bm_server_id,''), COALESCE(auto_forward,1), COALESCE(subdomain,''), COALESCE(host_mounts,''), COALESCE(autostart,1), COALESCE(watchdog,0), COALESCE(status_public,0), COALESCE(cpu_alarm_pct,0), COALESCE(mem_alarm_mb,0), COALESCE(disk_alarm_mb,0), COALESCE(tags,'')"

func scanServer(sc interface{ Scan(...any) error }) (serverRow, error) {
	var srv serverRow
	var installed, autoFwd, autostart, watchdog, statusPublic int
	var tags string
	err := sc.Scan(&srv.ID, &srv.Name, &srv.GameskillID, &srv.RealmID,
		&srv.Status, &srv.ContainerID, &srv.DataDir, &installed, &srv.InstallStatus, &srv.PortsJSON, &srv.CreatedAt, &srv.BMServerID, &autoFwd, &srv.Subdomain, &srv.HostMountsJSON, &autostart, &watchdog, &statusPublic, &srv.CPUAlarmPct, &srv.MemAlarmMB, &srv.DiskAlarmMB, &tags)
	srv.Tags = splitTags(tags)
	srv.Installed = installed == 1
	srv.AutoForward = autoFwd == 1
	srv.Autostart = autostart == 1
	srv.Watchdog = watchdog == 1
	srv.StatusPublic = statusPublic == 1
	srv.Ports = map[string]int{}
	json.Unmarshal([]byte(srv.PortsJSON), &srv.Ports)
	return srv, err
}

// splitTags parses the stored comma-separated tags into a slice (never nil, so it
// JSON-encodes as [] not null).
func splitTags(s string) []string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// normalizeTags cleans user-supplied tags — trim, lowercase, drop blanks, dedupe,
// and cap (20 tags, 30 chars each) — then joins them for storage.
func normalizeTags(in []string) string {
	seen := map[string]bool{}
	var out []string
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		t = strings.ReplaceAll(t, ",", " ") // commas are the separator; never inside a tag
		if t == "" || seen[t] {
			continue
		}
		if len(t) > 30 {
			t = t[:30]
		}
		seen[t] = true
		out = append(out, t)
		if len(out) >= 20 {
			break
		}
	}
	return strings.Join(out, ",")
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
		if admin {
			srv.Perms = allPermStrings
			list = append(list, srv)
		} else if rbac.VisibleServer(grants, srv.target()) {
			srv.Perms = permStrings(rbac.EffectivePerms(grants, srv.target()))
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
	if isAdmin(r) {
		srv.Perms = allPermStrings
		// Host bind mounts are an admin-only concern (host paths) — only echo them back to admins.
		if srv.HostMountsJSON != "" {
			_ = json.Unmarshal([]byte(srv.HostMountsJSON), &srv.HostMounts)
		}
	} else if c := claimsFromContext(r.Context()); c != nil {
		srv.Perms = permStrings(rbac.EffectivePerms(s.loadGrants(r.Context(), c.UserID), srv.target()))
	}
	// Single GET also returns the current variable values + resource caps so the
	// edit form can be pre-filled.
	var envJSON string
	var notesMD int
	s.db.QueryRowContext(r.Context(),
		"SELECT env_json, COALESCE(cpu_limit,0), COALESCE(mem_limit_mb,0), COALESCE(notes,''), COALESCE(notes_markdown,0) FROM servers WHERE id=?", id).
		Scan(&envJSON, &srv.CPUPercent, &srv.MemoryMB, &srv.Notes, &notesMD)
	srv.NotesMarkdown = notesMD == 1
	// Rendered here, not in the browser: the frontend carries no markdown library
	// (and no runtime dependencies at all), and the escaping is the security
	// boundary — it belongs where it can be tested. See notes_render.go.
	if srv.NotesMarkdown {
		srv.NotesHTML = renderNotes(srv.Notes)
	}
	srv.Env = map[string]string{}
	json.Unmarshal([]byte(envJSON), &srv.Env)
	// Mask ALL secret env vars (RCON password + password-typed vars) so they're
	// never echoed — neither plaintext nor the at-rest ciphertext — to anyone with
	// only ServerView. The update handler treats secretMask as "keep existing", so
	// the edit form round-trips without clobbering the real value.
	if rt, err := s.loadRuntime(r.Context(), id); err == nil {
		for k := range secretEnvKeys(rt.gs) {
			if srv.Env[k] != "" {
				srv.Env[k] = secretMask
			}
		}
		srv.WipeSupported = rt.gs.Wipe != nil
		srv.RestartWarn = rt.gs.Restart != nil && len(rt.gs.Restart.Warnings) > 0
		srv.WatchdogSup = rt.gs.Query != nil
		srv.PlayersSup = rt.gs.Players != nil
		srv.AdminLogSup = rt.gs.AdminLog != nil
		_, srv.ModsSupported = modProfileFor(rt.env["SERVER_TYPE"]) // Modrinth mod manager
		srv.ConfigFiles = rt.gs.ConfigFiles
	}
	// AI features (digest, error-explainer) are available whenever AI is enabled;
	// the digest button additionally lives inside the admin-log-gated Activity tab.
	srv.AIEnabled = s.aiEnabled(r.Context())
	jsonOK(w, srv)
}

// secretMask is returned in place of secret env values; the update handler keeps the
// existing value when it sees this sentinel (so the UI can round-trip without leaking).
const secretMask = "••••••••"

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
		Mods        *string           `json:"mods"` // alias for env["MODS"]; see handleUpdateServer
		CPUPercent  float64           `json:"cpu_percent"`
		MemoryMB    int64             `json:"memory_mb"`
		Subdomain   string            `json:"subdomain"`   // NPM subdomain for HTTP apps (empty = off)
		HostMounts  []hostMount       `json:"host_mounts"` // admin-only host bind mounts
		Autostart   *bool             `json:"autostart"`   // start on boot; nil = default on
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	// Host mounts bind host paths in — admin-only, and validated against a denylist.
	var hostMountsJSON string
	if len(req.HostMounts) > 0 {
		if !isAdmin(r) {
			jsonError(w, "host mounts require admin", http.StatusForbidden)
			return
		}
		cleaned, err := s.validateHostMounts(req.HostMounts)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if b, err := json.Marshal(cleaned); err == nil {
			hostMountsJSON = string(b)
		}
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
	if req.Mods != nil {
		env["MODS"] = strings.TrimSpace(*req.Mods)
	}

	// Allocate ports. Seed "taken" with host ports Docker already publishes (incl.
	// orphaned containers), track picks within this request, and test-bind each
	// candidate — so we never hand out a port that can't actually be bound.
	allocatedPorts := map[string]int{}
	if err := validateEnv(gs, env); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

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

	s.encryptSecretEnv(env, gs) // encrypt secret-typed values before persisting
	envJSON, _ := json.Marshal(env)
	portsJSON, _ := json.Marshal(allocatedPorts)
	realmID := req.RealmID
	if realmID == "" {
		realmID = s.ensureRealm(r.Context(), gs.Category)
	}

	_, err = s.db.ExecContext(r.Context(), `
		INSERT INTO servers (id, name, gameskill_id, realm_id, status, env_json, ports_json, cpu_limit, mem_limit_mb, data_dir, subdomain)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
	`, serverID, req.Name, req.GameskillID, nullableStr(realmID),
		"stopped", string(envJSON), string(portsJSON),
		req.CPUPercent, req.MemoryMB, dataDir, normalizeSubdomain(req.Subdomain))
	if err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if hostMountsJSON != "" {
		s.db.ExecContext(r.Context(), "UPDATE servers SET host_mounts=? WHERE id=?", hostMountsJSON, serverID)
	}
	// Autostart defaults to on (the column default); only persist when explicitly off.
	if req.Autostart != nil && !*req.Autostart {
		s.db.ExecContext(r.Context(), "UPDATE servers SET autostart=0 WHERE id=?", serverID)
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
	s.syncRuneWatchers(r.Context(), serverID, gs)

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
		Name    *string           `json:"name"`
		RealmID *string           `json:"realm_id"`
		Env     map[string]string `json:"env"`
		// Mods is a convenience alias for env["MODS"] (semicolon-separated Workshop
		// IDs in load order). The web UI sends mods inside env, but API clients
		// naturally reach for a top-level "mods" — accept both so it can't silently
		// no-op.
		Mods          *string      `json:"mods"`
		CPUPercent    *float64     `json:"cpu_percent"`
		MemoryMB      *int64       `json:"memory_mb"`
		BMServerID    *string      `json:"bm_server_id"`
		AutoForward   *bool        `json:"auto_forward"`
		Autostart     *bool        `json:"autostart"`
		StatusPublic  *bool        `json:"status_public"`
		CPUAlarmPct   *int         `json:"cpu_alarm_pct"`
		MemAlarmMB    *int         `json:"mem_alarm_mb"`
		DiskAlarmMB   *int         `json:"disk_alarm_mb"`
		Notes         *string      `json:"notes"`
		NotesMarkdown *bool        `json:"notes_markdown"`
		Tags          *[]string    `json:"tags"`
		Subdomain     *string      `json:"subdomain"`
		HostMounts    *[]hostMount `json:"host_mounts"` // admin-only; nil = leave unchanged
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	// Check the submitted variables before anything is written: this handler
	// applies each field with its own UPDATE, so failing at the env block would
	// leave an earlier one (the name, the realm) already changed.
	//
	// Only what's being changed is checked — not the merged result — so a rune
	// that tightened its bounds after a server was built doesn't make every later
	// edit fail on a field nobody touched.
	if len(req.Env) > 0 {
		if rt, err := s.loadRuntime(r.Context(), id); err == nil {
			changed := map[string]string{}
			for k, v := range req.Env {
				if v == secretMask {
					continue // unchanged masked secret — not a new value to check
				}
				changed[k] = v
			}
			if err := validateEnv(rt.gs, changed); err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
	}
	if req.HostMounts != nil {
		// Host mounts expose host paths to a semi-trusted container — admin-only,
		// validated against a denylist. Applies on the next container recreate.
		if !isAdmin(r) {
			jsonError(w, "host mounts require admin", http.StatusForbidden)
			return
		}
		cleaned, err := s.validateHostMounts(*req.HostMounts)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := json.Marshal(cleaned)
		s.db.ExecContext(r.Context(), "UPDATE servers SET host_mounts=? WHERE id=?", string(b), id)
	}
	if req.Subdomain != nil {
		sub := normalizeSubdomain(*req.Subdomain)
		// If the subdomain changed and a proxy host exists, drop it; the next start
		// re-creates one for the new domain (and clears npm_host_id when cleared).
		if sub != srv.Subdomain {
			go s.npmRemoveServer(id)
			go s.cfRemoveServer(id)
		}
		s.db.ExecContext(r.Context(), "UPDATE servers SET subdomain=? WHERE id=?", sub, id)
	}
	if req.BMServerID != nil {
		s.db.ExecContext(r.Context(), "UPDATE servers SET bm_server_id=? WHERE id=?", strings.TrimSpace(*req.BMServerID), id)
	}
	if req.AutoForward != nil {
		s.db.ExecContext(r.Context(), "UPDATE servers SET auto_forward=? WHERE id=?", boolInt(*req.AutoForward), id)
	}
	if req.Autostart != nil {
		s.db.ExecContext(r.Context(), "UPDATE servers SET autostart=? WHERE id=?", boolInt(*req.Autostart), id)
		// Apply it to the live container too, so the toggle takes effect on the next
		// reboot rather than only after the server is next recreated. Otherwise a
		// server you just told not to auto-start keeps its old on-failure policy and
		// Docker still brings it back — the bug this setting is supposed to control.
		if cid := s.containerID(id); cid != "" {
			if err := s.docker.SetRestartPolicy(r.Context(), cid, *req.Autostart); err != nil {
				log.Printf("autostart: could not update restart policy for %s: %v", id, err)
			}
		}
	}
	if req.StatusPublic != nil {
		s.db.ExecContext(r.Context(), "UPDATE servers SET status_public=? WHERE id=?", boolInt(*req.StatusPublic), id)
		// Sharing is a privacy decision, so it takes effect now rather than
		// whenever the board's cache happens to expire. Un-sharing especially:
		// leaving a server listed for another 15s after you've said to stop is
		// the wrong way for that toggle to fail.
		s.invalidateStatusCache()
	}
	if req.CPUAlarmPct != nil {
		v := *req.CPUAlarmPct
		if v < 0 {
			v = 0
		}
		s.db.ExecContext(r.Context(), "UPDATE servers SET cpu_alarm_pct=? WHERE id=?", v, id)
	}
	if req.MemAlarmMB != nil {
		v := *req.MemAlarmMB
		if v < 0 {
			v = 0
		}
		s.db.ExecContext(r.Context(), "UPDATE servers SET mem_alarm_mb=? WHERE id=?", v, id)
	}
	if req.DiskAlarmMB != nil {
		v := *req.DiskAlarmMB
		if v < 0 {
			v = 0
		}
		s.db.ExecContext(r.Context(), "UPDATE servers SET disk_alarm_mb=? WHERE id=?", v, id)
	}
	if req.NotesMarkdown != nil {
		s.db.ExecContext(r.Context(), "UPDATE servers SET notes_markdown=? WHERE id=?", boolInt(*req.NotesMarkdown), id)
	}
	if req.Notes != nil {
		notes := *req.Notes
		if len(notes) > 8000 { // keep it a note, not a document
			notes = notes[:8000]
		}
		s.db.ExecContext(r.Context(), "UPDATE servers SET notes=? WHERE id=?", notes, id)
	}
	if req.Tags != nil {
		s.db.ExecContext(r.Context(), "UPDATE servers SET tags=? WHERE id=?", normalizeTags(*req.Tags), id)
	}
	if req.Name != nil && *req.Name != "" {
		s.db.ExecContext(r.Context(), "UPDATE servers SET name=? WHERE id=?", *req.Name, id)
	}
	if req.RealmID != nil {
		// realm_id is a permission-scoping field: moving a server between realms
		// changes which delegates can reach it, so it's admin-only — a delegate
		// with mere ServerControl must not be able to reassign scope (like the
		// host_mounts block above).
		if !isAdmin(r) {
			jsonError(w, "changing realm requires admin", http.StatusForbidden)
			return
		}
		s.db.ExecContext(r.Context(), "UPDATE servers SET realm_id=? WHERE id=?", nullableStr(*req.RealmID), id)
	}
	if req.Env != nil || req.Mods != nil {
		// Merge onto the existing env so unspecified vars are preserved. The
		// existing secret values stay encrypted through the merge; encryptSecretEnv
		// below encrypts only the newly-supplied (plaintext) ones (idempotent).
		var gs *gameskill.Gameskill
		if rt, err := s.loadRuntime(r.Context(), id); err == nil {
			gs = rt.gs
		}
		current := map[string]string{}
		var envJSON string
		s.db.QueryRowContext(r.Context(), "SELECT env_json FROM servers WHERE id=?", id).Scan(&envJSON)
		json.Unmarshal([]byte(envJSON), &current)
		for k, v := range req.Env {
			if v == secretMask {
				continue // unchanged masked secret (e.g. RCON password) — keep existing
			}
			current[k] = v
		}
		// Top-level "mods" wins over env["MODS"] when both are sent.
		if req.Mods != nil {
			current["MODS"] = strings.TrimSpace(*req.Mods)
		}
		s.encryptSecretEnv(current, gs)
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
	s.unifiRemoveServer(id)
	s.npmRemoveServer(id) // sync: reads npm_host_id before the row is deleted
	s.cfRemoveServer(id)  // sync: reads cf_hostname before the row is deleted
	// Capture the gameskill image now (DB row still present) in case we need a
	// root container to delete root-owned files left by a failed install.
	var rmImage string
	var rmGS *gameskill.Gameskill
	if rt, err := s.loadRuntime(r.Context(), id); err == nil {
		rmImage = rt.gs.Docker.Image
		rmGS = rt.gs
	}
	if srv.ContainerID != "" {
		_ = s.docker.Remove(r.Context(), srv.ContainerID)
	}
	// Tear down any app-stack sidecars + their network (no-op for single-container
	// runes). The sidecars' data dirs live under the server data dir removed below.
	if rmGS != nil {
		s.removeStack(r.Context(), id, rmGS)
	}
	s.db.ExecContext(r.Context(), "DELETE FROM port_allocations WHERE server_id=?", id)
	s.db.ExecContext(r.Context(), "DELETE FROM servers WHERE id=?", id)
	s.clearWatchdog(id)
	s.clearStartWatch(id)
	// Reclaim the disk: remove the server's data directory (game files, world,
	// configs). Without this, deleted servers leave multi-GB dirs behind and the
	// disk fills up. Guard against an empty/relative path so we never rm /data.
	if srv.DataDir != "" && filepath.IsAbs(srv.DataDir) && strings.Contains(srv.DataDir, "servers") {
		if err := os.RemoveAll(srv.DataDir); err != nil {
			// A failed Steam install can leave root/steam-owned files the panel
			// user can't delete. Empty the dir via a root container, then remove it.
			if rmImage != "" {
				s.docker.RunEphemeralOpts(r.Context(), docker.EphemeralOptions{
					Image:   rmImage,
					DataDir: srv.DataDir,
					Script:  "rm -rf /data/* /data/.[!.]* /data/..?* 2>/dev/null || true",
					User:    "0:0",
				}, io.Discard) //nolint:errcheck
			}
			if err2 := os.RemoveAll(srv.DataDir); err2 != nil {
				s.install.publish(id, "WARN: could not remove data dir: "+err2.Error())
			}
		}
	}
	s.auditLog(r, "server.delete", "server:"+id, nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}

// recreateAndStart (re)creates this server's container from its CURRENT rune, env,
// and allocated ports, then starts it (a watcher promotes it to "running"). Any
// existing container is removed first — so this is the single code path that
// actually applies rune/env/mod changes. A plain `docker restart` keeps the old
// baked-in command/env, which is why restart alone never picked up changes. Shared
// by start, restart, and the post-(re)install refresh. The caller is responsible
// for permission + install-state checks.
// Graceful-stop grace period (SIGTERM→SIGKILL) when a rune doesn't set one, and
// the ceiling a rune may request. DayZ flushes its whole Central Economy on
// shutdown, so a couple of seconds isn't enough — the default is generous and a
// rune can raise it (dayz: 90) up to the cap.
const (
	defaultStopTimeout = 30
	maxStopTimeout     = 300
)

// stopTimeout resolves a rune's SIGTERM→SIGKILL grace period, clamped to sane bounds.
func stopTimeout(gs *gameskill.Gameskill) int {
	t := gs.Startup.StopTimeout
	if t <= 0 {
		return defaultStopTimeout
	}
	if t > maxStopTimeout {
		return maxStopTimeout
	}
	return t
}

// gracefulStop shuts a server's container down as cleanly as the rune allows: it
// sends the rune's save_command (flush state) then its stop command over the
// console, gives the game a moment to act, and finally docker-stops with the
// rune's grace period before SIGKILL. Centralizes what restart/stop both need so
// a game like DayZ isn't killed mid-persistence-save. A rune that declares
// neither command just gets the (longer, rune-tunable) SIGTERM grace.
func (s *Server) gracefulStop(ctx context.Context, containerID string, gs *gameskill.Gameskill) error {
	if gs.Startup.SaveCommand != "" {
		s.docker.SendStdin(ctx, containerID, gs.Startup.SaveCommand)
		time.Sleep(2 * time.Second) // let the save flush before we ask it to stop
	}
	if gs.Startup.Stop != "" {
		s.docker.SendStdin(ctx, containerID, gs.Startup.Stop)
		time.Sleep(2 * time.Second)
	}
	return s.docker.Stop(ctx, containerID, stopTimeout(gs))
}

func (s *Server) recreateAndStart(ctx context.Context, id string) error {
	srv, err := s.getServer(ctx, id)
	if err != nil {
		return err
	}
	rt, err := s.loadRuntime(ctx, id)
	if err != nil {
		return fmt.Errorf("load runtime: %w", err)
	}
	gs, env, ports := rt.gs, rt.env, rt.ports

	// Remove any existing container first (its ports free up for reconcile below).
	// Stop gracefully so games that flush persistence on shutdown (DayZ's Central
	// Economy) aren't killed mid-save — the old path used a hard 5s SIGKILL.
	if srv.ContainerID != "" {
		s.gracefulStop(ctx, srv.ContainerID, gs)
		s.docker.Remove(ctx, srv.ContainerID)
		s.db.ExecContext(ctx, "UPDATE servers SET container_id='' WHERE id=?", id)
		srv.ContainerID = ""
	}
	// Reconcile host ports: if a previously-allocated port is now held by another
	// container/process, reallocate it so the container can bind.
	reallocated := false
	dockerUsed, _ := s.docker.UsedHostPorts(ctx)
	taken := map[int]bool{}
	for _, hp := range ports {
		taken[hp] = true
	}
	for name, hp := range ports {
		if !dockerUsed[hp] && hostPortAvailable(hp) {
			continue
		}
		delete(taken, hp)
		newPort, aerr := s.allocatePort(ctx, hp, taken)
		if aerr != nil {
			return fmt.Errorf("port reconcile: %w", aerr)
		}
		ports[name] = newPort
		taken[newPort] = true
		s.db.ExecContext(ctx, "DELETE FROM port_allocations WHERE server_id=? AND name=?", id, name)
		s.db.ExecContext(ctx, "INSERT INTO port_allocations (port, server_id, name) VALUES (?,?,?)", newPort, id, name)
		reallocated = true
	}
	if reallocated {
		portsJSON, _ := json.Marshal(ports)
		s.db.ExecContext(ctx, "UPDATE servers SET ports_json=? WHERE id=?", string(portsJSON), id)
	}

	// Build docker env slice (server vars + PORT_<name> helpers). HOME=/data gives
	// the (non-root) runtime user a writable home for caches.
	envSlice := []string{"HOME=/data"}
	// Fixed rune env (e.g. an app stack's connection strings) first, so a user
	// variable of the same key overrides it.
	for k, v := range gs.Docker.Env {
		envSlice = append(envSlice, fmt.Sprintf("%s=%s", k, gameskill.ApplyTemplate(v, env)))
	}
	for k, v := range env {
		envSlice = append(envSlice, fmt.Sprintf("%s=%s", k, v))
	}
	for name, port := range ports {
		envSlice = append(envSlice, fmt.Sprintf("PORT_%s=%d", name, port))
	}

	// Steam games publish their port 1:1 (bind==advertised); others use the
	// gameskill's fixed default container port.
	steamGame := gs.Steam != nil
	portMappings := []docker.PortMapping{}
	for _, p := range gs.Ports {
		if hostPort, ok := ports[p.Name]; ok {
			containerPort := p.Default
			if steamGame {
				containerPort = hostPort
			}
			portMappings = append(portMappings, docker.PortMapping{
				HostPort:      hostPort,
				ContainerPort: containerPort,
				Protocol:      p.Protocol,
			})
		}
	}

	image := gameskill.ApplyTemplate(gs.Docker.Image, env)
	var cmd []string
	if len(gs.Startup.Exec) > 0 {
		// Raw argv (no shell) — for distroless/ko images or passing args to the
		// image's own ENTRYPOINT. Template each element.
		for _, a := range gs.Startup.Exec {
			cmd = append(cmd, gameskill.ApplyTemplate(a, env))
		}
	} else if startupCmd := gameskill.ApplyTemplate(gs.Startup.Command, env); startupCmd != "" {
		cmd = []string{"/bin/sh", "-c", startupCmd}
	}
	containerName := fmt.Sprintf("ygg-%s", id[:8])

	var cpuLimit float64
	var memLimit int64
	s.db.QueryRowContext(ctx,
		"SELECT COALESCE(cpu_limit,0), COALESCE(mem_limit_mb,0) FROM servers WHERE id=?", id).
		Scan(&cpuLimit, &memLimit)

	// Clear any orphaned container with our deterministic name so Create can't fail
	// on a name conflict (the container is always recreated fresh; state is in /data).
	s.docker.Remove(ctx, containerName)
	s.docker.PullImage(ctx, image, io.Discard)

	// App stacks: bring up the sidecar dependencies (db, cache) on a private network
	// first, and join the main container to it so it resolves them by name. A no-op
	// for ordinary single-container runes (len(Services) == 0).
	stackNet := ""
	if len(gs.Services) > 0 {
		if err := s.startStack(ctx, id, srv.DataDir, gs, env); err != nil {
			return fmt.Errorf("start stack: %w", err)
		}
		stackNet = stackNetworkName(id)
	}

	runtimeUser := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	if gs.Docker.User != "" {
		runtimeUser = gameskill.ApplyTemplate(gs.Docker.User, env)
	}
	containerID, err := s.docker.Create(ctx, docker.CreateOptions{
		Name:           containerName,
		Image:          image,
		Env:            envSlice,
		Cmd:            cmd,
		User:           runtimeUser,
		Ports:          portMappings,
		DataDir:        srv.DataDir,
		DataMount:      gs.Docker.DataPath, // empty = /data
		ExtraVolumes:   gs.Docker.ExtraVolumes,
		KeepEntrypoint: gs.Docker.KeepEntrypoint,
		Autostart:      srv.Autostart, // off → no Docker restart policy (won't come back on reboot)
		Capabilities:   gs.Docker.Capabilities,
		Devices:        gs.Docker.Devices,
		Sysctls:        gs.Docker.Sysctls,
		HostMounts:     s.loadHostMounts(ctx, id), // admin-set host binds (validated on save)
		CPUPercent:     cpuLimit,
		MemoryMB:       memLimit,
		Labels:         map[string]string{"yggdrasil.server_id": id},
		Network:        stackNet, // "" for single-container runes = default bridge
	})
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	if err := s.docker.Start(ctx, containerID); err != nil {
		return fmt.Errorf("start container: %w", err)
	}

	// Mark "starting"; the watcher promotes to "running" on the done_regex.
	s.db.ExecContext(ctx,
		"UPDATE servers SET status='starting', container_id=? WHERE id=?",
		containerID, id)
	go s.watchStartupReady(id, containerID, gs.Startup.DoneRegex)
	// Only open firewall ports when this server opts in (default on).
	if srv.AutoForward {
		go s.upnpAddServer(id, srv.Name)
		go s.unifiAddServer(id, srv.Name)
	}
	// Subdomain routing is independent of firewall forwarding (NPM/Cloudflare each
	// handle public exposure themselves); both self-gate on enabled + a subdomain.
	go s.npmAddServer(id, srv.Name)
	go s.cfAddServer(id, srv.Name)
	return nil
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

	if err := s.recreateAndStart(r.Context(), id); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A manual start clears any watchdog quarantine — give auto-heal a fresh chance —
	// and resets the failed-start streak so this attempt gets the full retry budget.
	s.clearWatchdog(id)
	s.clearStartWatch(id)

	s.auditLog(r, "server.start", "server:"+id, nil)
	s.notifyServer(id, "▶️ " + srv.Name + " started")
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
		// Graceful shutdown: flush + stop via the rune's console commands, then
		// SIGTERM with the rune's grace period, so the world saves before it stops.
		if rt, err := s.loadRuntime(r.Context(), id); err == nil {
			if err := s.gracefulStop(r.Context(), srv.ContainerID, rt.gs); err != nil {
				jsonError(w, "stop failed: "+err.Error(), http.StatusBadGateway)
				return
			}
			s.stopStack(r.Context(), id, rt.gs) // bring down sidecars too (no-op if none)
		} else if err := s.docker.Stop(r.Context(), srv.ContainerID, defaultStopTimeout); err != nil {
			jsonError(w, "stop failed: "+err.Error(), http.StatusBadGateway)
			return
		}
	}
	s.db.ExecContext(r.Context(), "UPDATE servers SET status='stopped' WHERE id=?", id)
	// A deliberate stop cancels any pending start-retry chain / streak + alarm state.
	s.clearStartWatch(id)
	s.clearResourceAlarms(id)
	go s.upnpRemoveServer(id)
	go s.unifiRemoveServer(id)
	go s.npmRemoveServer(id)
	go s.cfRemoveServer(id)
	s.auditLog(r, "server.stop", "server:"+id, nil)
	s.notifyServer(id, "⏹️ " + srv.Name + " stopped")
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
	if s.install.isActive(id) {
		jsonError(w, "install in progress; please wait", http.StatusConflict)
		return
	}
	var installed int
	s.db.QueryRowContext(r.Context(), "SELECT installed FROM servers WHERE id=?", id).Scan(&installed)
	if installed == 0 {
		jsonError(w, "server is not installed yet", http.StatusConflict)
		return
	}
	// Recreate the container (not a plain docker-restart) so any rune/env/mod change
	// since it was last created actually takes effect.
	if err := s.recreateAndStart(r.Context(), id); err != nil {
		jsonError(w, "restart failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.auditLog(r, "server.restart", "server:"+id, nil)
	s.notifyServer(id, "🔄 " + srv.Name + " restarted")
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
		if err := conn.WriteMessage(websocket.TextMessage, []byte(stripANSI(sc.Text()))); err != nil {
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

	// WebSocket input → RCON, or the container's stdin (one command per message)
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		command := strings.TrimSpace(string(msg))
		if command == "" {
			continue
		}
		for _, line := range s.consoleSend(ctx, id, command, hijack.Conn) {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
				return
			}
		}
	}
}

// consoleSend delivers one console command to a server and returns the lines to
// echo back to the operator (empty when the command went to stdin, which reports
// itself through the container's own log stream).
//
// It prefers RCON wherever the rune declares one. Rust reads commands over its
// WebSocket RCON and DayZ over BattlEye; neither reads container stdin, so a
// command written there is swallowed with no error and no effect. Games with no
// rcon: block (Bedrock) keep using stdin, which is their real control channel.
//
// An RCON failure falls back to stdin rather than erroring out: Minecraft stamps
// rcon.password into server.properties at install time only, so a password
// changed in the panel afterwards fails to authenticate while stdin still works.
// The fallback keeps that console usable — but it says so, because a command that
// silently goes nowhere is the bug this function exists to fix.
func (s *Server) consoleSend(ctx context.Context, serverID, command string, stdin io.Writer) []string {
	reply, err := s.rconExec(ctx, serverID, command)
	if err == nil {
		// Plenty of commands answer with nothing; don't echo a blank line for them.
		if reply = strings.TrimRight(reply, "\n"); reply == "" {
			return nil
		}
		return strings.Split(reply, "\n")
	}
	var out []string
	if !errors.Is(err, errNoRCON) {
		out = append(out, "[rcon unavailable: "+err.Error()+" — sent to the container console instead]")
	}
	if _, err := stdin.Write([]byte(command + "\n")); err != nil {
		return append(out, "[could not send the command: "+err.Error()+"]")
	}
	return out
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
		if conn.WriteMessage(websocket.TextMessage, []byte(stripANSI(sc.Text()))) != nil {
			return
		}
	}
}

// Port allocation
// allocatePort returns a free host port from the configured range. A port is free
// only if it is not in the port_allocations table, not in `taken` (already
// chosen earlier in this request), and not actually bound on the host.
// `preferred` is accepted but ignored — see the comment in the body.
func (s *Server) allocatePort(ctx context.Context, preferred int, taken map[int]bool) (int, error) {
	// Allocate sequentially from the configured range, NOT the game's well-known
	// default port (2302, 25565, 27016, …) — distinctive ports are less scanned/
	// abused, and each server still gets its own unique one. `preferred` is
	// ignored on purpose (kept in the signature for callers/tests).
	_ = preferred
	for port := s.cfg.Ports.RangeMin; port <= s.cfg.Ports.RangeMax; port++ {
		if !taken[port] && s.portAvailable(ctx, port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free ports in range %d-%d", s.cfg.Ports.RangeMin, s.cfg.Ports.RangeMax)
}

// portAvailable reports whether a single host port is free to claim, ignoring any
// in-flight allocation set: it's neither recorded in port_allocations nor bindable
// on the host right now. Callers track their own just-claimed ports separately.
func (s *Server) portAvailable(ctx context.Context, port int) bool {
	var count int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM port_allocations WHERE port=?", port).Scan(&count)
	if count != 0 {
		return false
	}
	return hostPortAvailable(port)
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
