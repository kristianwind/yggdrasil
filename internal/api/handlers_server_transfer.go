package api

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

const transferVersion = 2 // v2 adds schedules, watchers, notification routing and subdomain

// transferManifest is a single server's portable setup: enough to recreate it on
// another panel, plus the rune so the target doesn't need it pre-installed. Env
// secrets are DECRYPTED here (the source re-encrypts nothing; the target
// re-encrypts with its own key), which makes the bundle a credential — admin-only
// on both ends, and it should be handled like one.
type transferManifest struct {
	Version     int               `json:"version"`
	Name        string            `json:"name"`
	GameskillID string            `json:"gameskill_id"`
	RealmName   string            `json:"realm_name"` // the group; matched by name on import (ids don't cross panels)
	RuneYAML    string            `json:"rune_yaml"`
	Env         map[string]string `json:"env"`
	Ports       map[string]int    `json:"ports,omitempty"` // source host ports by name; a migration keeps them so NPM/tunnel/DNS forwarding survives — reallocated only on collision
	CPULimit    float64           `json:"cpu_limit"`
	MemLimitMB  int64             `json:"mem_limit_mb"`
	HostMounts  string            `json:"host_mounts"`
	Autostart   int               `json:"autostart"`
	AutoForward int               `json:"auto_forward"`
	Watchdog    int               `json:"watchdog"`
	// v2: the server's operational tail — everything an admin set up around the
	// server, so a move carries the whole habitat, not just the animal.
	Subdomain string             `json:"subdomain,omitempty"`
	Schedules []transferSchedule `json:"schedules,omitempty"`
	Watchers  []transferWatcher  `json:"watchers,omitempty"`
	Channels  []transferChannel  `json:"channels,omitempty"` // server-scoped notification channels; config DECRYPTED like env secrets
}

type transferSchedule struct {
	Name     string `json:"name"`
	CronExpr string `json:"cron_expr"`
	Action   string `json:"action"`
	ArgsJSON string `json:"args_json"`
	Enabled  int    `json:"enabled"`
}

type transferWatcher struct {
	Name       string `json:"name"`
	Pattern    string `json:"pattern"`
	Threshold  int    `json:"threshold"`
	WindowSecs int    `json:"window_secs"`
	Action     string `json:"action"`
	Enabled    int    `json:"enabled"`
	Source     string `json:"source,omitempty"`
}

type transferChannel struct {
	Type    string `json:"type"`
	Config  string `json:"config"` // decrypted; the target re-encrypts
	Enabled int    `json:"enabled"`
}

// handleServerExport streams one server as a portable bundle: its config (with
// secrets decrypted), its rune, and its data directory. Admin-only because it
// exposes plaintext secrets that even a ServerControl delegate can't normally see.
func (s *Server) handleServerExport(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	src, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !isAdmin(r) {
		jsonError(w, "forbidden: exporting a server exposes its secrets (admin only)", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", safeFilename(src.Name)+".yggserver.tar.gz"))
	if err := s.writeServerBundle(r.Context(), w, id); err != nil {
		return // headers are gone; the truncated stream is the only signal left
	}
	s.auditLog(r, "server.export", "server:"+id, map[string]string{"name": src.Name})
}

// buildTransferManifest assembles a server's portable manifest (secrets
// decrypted) and returns it with the server's data dir.
func (s *Server) buildTransferManifest(ctx context.Context, id string) (*transferManifest, string, error) {
	src, err := s.getServer(ctx, id)
	if err != nil {
		return nil, "", err
	}

	var envJSON, hostMounts, yamlBlob, realmName string
	var cpu float64
	var mem int64
	var autostart, autoForward, watchdog int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(s.env_json,'{}'), COALESCE(s.host_mounts,''), COALESCE(s.cpu_limit,0), COALESCE(s.mem_limit_mb,0),
		       COALESCE(s.autostart,1), COALESCE(s.auto_forward,1), COALESCE(s.watchdog,0), g.yaml_blob, COALESCE(rlm.name,'')
		FROM servers s JOIN gameskills g ON g.id=s.gameskill_id
		LEFT JOIN realms rlm ON rlm.id=s.realm_id WHERE s.id=?`, id).
		Scan(&envJSON, &hostMounts, &cpu, &mem, &autostart, &autoForward, &watchdog, &yamlBlob, &realmName); err != nil {
		return nil, "", err
	}
	gs, err := gameskill.Parse([]byte(yamlBlob))
	if err != nil {
		return nil, "", err
	}
	env := map[string]string{}
	json.Unmarshal([]byte(envJSON), &env)
	s.decryptSecretEnv(env, gs) // plaintext into the bundle; the target re-encrypts

	man := transferManifest{
		Version: transferVersion, Name: src.Name, GameskillID: src.GameskillID, RealmName: realmName, RuneYAML: yamlBlob,
		Env: env, Ports: src.Ports, CPULimit: cpu, MemLimitMB: mem, HostMounts: hostMounts,
		Autostart: autostart, AutoForward: autoForward, Watchdog: watchdog,
		Subdomain: src.Subdomain,
	}
	s.collectServerTail(ctx, id, &man)
	return &man, src.DataDir, nil
}

// writeServerBundle streams one server's portable bundle (manifest + data dir)
// to w — the single-server download format.
func (s *Server) writeServerBundle(ctx context.Context, out io.Writer, id string) error {
	man, dataDir, err := s.buildTransferManifest(ctx, id)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	manJSON, _ := json.MarshalIndent(man, "", "  ")
	tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o644, Size: int64(len(manJSON))})
	tw.Write(manJSON)
	if dataDir != "" {
		if fi, err := os.Stat(dataDir); err == nil && fi.IsDir() {
			tarDataDir(tw, dataDir, "data/") // best-effort; a read error just truncates the data
		}
	}
	tw.Close()
	return gz.Close()
}

// handleServerImport creates a new server on this panel from an exported bundle:
// adds the rune if missing, re-encrypts the secrets with this panel's key,
// preserves the source's host ports (reallocating only the ones already taken
// here), restores the data directory, and marks it installed. Admin-only.
func (s *Server) handleServerImport(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	resp, err := s.importServerBundle(r.Context(), io.LimitReader(r.Body, 8<<30), false)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if resp["skipped"] == true {
		jsonOK(w, resp)
		return
	}
	s.auditLog(r, "server.import", "server:"+fmt.Sprint(resp["id"]), map[string]string{"name": fmt.Sprint(resp["name"])})
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, resp)
}

// importServerBundle reads one .yggserver.tar.gz stream and creates the server.
// skipExisting makes it a no-op (reported, not an error) when a server of the
// bundle's name already lives here — the bulk migration wants idempotence. The
// imported server keeps its own name when free; only a clash appends
// " (imported)".
func (s *Server) importServerBundle(ctx context.Context, body io.Reader, skipExisting bool) (map[string]any, error) {
	gz, err := gzip.NewReader(body)
	if err != nil {
		return nil, fmt.Errorf("not a valid bundle")
	}
	tr := tar.NewReader(gz)

	newID := uuid.New().String()
	base := s.cfg.Database.Path[:len(s.cfg.Database.Path)-len(filepath.Base(s.cfg.Database.Path))]
	dataDir := filepath.Join(base, "servers", newID)

	var man *transferManifest
	skipData := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			os.RemoveAll(dataDir)
			return nil, fmt.Errorf("corrupt bundle")
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			os.RemoveAll(dataDir)
			return nil, fmt.Errorf("unsafe bundle entry")
		}
		if name == "manifest.json" {
			b, _ := io.ReadAll(tr)
			var m transferManifest
			if json.Unmarshal(b, &m) != nil || m.Version < 1 || m.Version > transferVersion {
				return nil, fmt.Errorf("unsupported bundle version")
			}
			man = &m
			// The manifest precedes data/ in our bundles, so the skip decision can
			// land before gigabytes of data are unpacked for nothing.
			if skipExisting && s.serverNameTaken(ctx, m.Name) {
				skipData = true
			}
			continue
		}
		if skipData {
			continue // drain without writing; the archive reader must still advance
		}
		if rel := strings.TrimPrefix(name, "data/"); rel != name {
			dest := filepath.Join(dataDir, filepath.Clean("/"+rel))
			if hdr.Typeflag == tar.TypeDir {
				os.MkdirAll(dest, 0o755)
				continue
			}
			os.MkdirAll(filepath.Dir(dest), 0o755)
			f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, hdr.FileInfo().Mode())
			if err == nil {
				io.Copy(f, tr)
				f.Close()
			}
		}
	}
	if man == nil {
		os.RemoveAll(dataDir)
		return nil, fmt.Errorf("no manifest in bundle")
	}
	if skipData {
		os.RemoveAll(dataDir)
		return map[string]any{"name": man.Name, "skipped": true, "reason": "a server with this name already exists"}, nil
	}
	return s.finalizeServerImport(ctx, man, newID, dataDir)
}

// finalizeServerImport turns an unpacked manifest + data dir into a real server
// row: rune ensured, ports picked, secrets re-encrypted, tail restored.
func (s *Server) finalizeServerImport(ctx context.Context, man *transferManifest, newID, dataDir string) (map[string]any, error) {
	// Ensure the rune exists — add it (as a community rune) when the target panel
	// doesn't already have it.
	gs, err := gameskill.Parse([]byte(man.RuneYAML))
	if err != nil {
		os.RemoveAll(dataDir)
		return nil, fmt.Errorf("bundle rune parse error")
	}
	var have int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM gameskills WHERE id=?", man.GameskillID).Scan(&have)
	if have == 0 {
		s.db.ExecContext(ctx,
			"INSERT INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES (?,?,?,?,?,0)",
			gs.ID, gs.Name, gs.Category, gs.Version, man.RuneYAML)
	} else {
		// Parse the panel's existing copy — its ports/secret-keys are what we honour.
		var yb string
		s.db.QueryRowContext(ctx, "SELECT yaml_blob FROM gameskills WHERE id=?", man.GameskillID).Scan(&yb)
		if g2, e := gameskill.Parse([]byte(yb)); e == nil {
			gs = g2
		}
	}

	// Keep the source's host ports where they're still free on this panel — a
	// migration must preserve game.host:PORT so NPM/tunnel/DNS forwarding survives
	// untouched. Only a real collision forces a fresh port, and those are reported
	// back so the admin can repoint just those.
	taken, _ := s.docker.UsedHostPorts(ctx)
	allocated, moved, err := pickTransferPorts(gs.Ports, man.Ports, taken,
		s.cfg.Ports.RangeMin, s.cfg.Ports.RangeMax,
		func(port int) bool { return s.portAvailable(ctx, port) })
	if err != nil {
		os.RemoveAll(dataDir)
		return nil, fmt.Errorf("port allocation failed: %v", err)
	}

	// Re-encrypt secrets with THIS panel's key.
	env := man.Env
	if env == nil {
		env = map[string]string{}
	}
	s.encryptSecretEnv(env, gs)
	envJSON, _ := json.Marshal(env)
	portsJSON, _ := json.Marshal(allocated)

	// Group the server: reuse the source's group by name (realms are keyed by name,
	// so this works across panels), falling back to the rune's category — exactly
	// where a freshly-created server of this rune would land. Never leave it ungrouped.
	realmName := man.RealmName
	if realmName == "" {
		realmName = gs.Category
	}
	realmID := s.ensureRealm(ctx, realmName)

	finalName := man.Name
	if s.serverNameTaken(ctx, finalName) {
		finalName = man.Name + " (imported)"
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO servers (id, name, gameskill_id, realm_id, status, env_json, ports_json, cpu_limit, mem_limit_mb,
		                     data_dir, host_mounts, autostart, auto_forward, watchdog, installed, install_status)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,'done')`,
		newID, finalName, man.GameskillID, nullableStr(realmID), "stopped", string(envJSON), string(portsJSON),
		man.CPULimit, man.MemLimitMB, dataDir, man.HostMounts, man.Autostart, man.AutoForward, man.Watchdog); err != nil {
		os.RemoveAll(dataDir)
		return nil, fmt.Errorf("db error: %v", err)
	}
	for portName, hostPort := range allocated {
		proto := "tcp"
		for _, p := range gs.Ports {
			if p.Name == portName {
				proto = p.Protocol
			}
		}
		s.db.ExecContext(ctx, "INSERT INTO port_allocations (port, server_id, protocol, name) VALUES (?,?,?,?)",
			hostPort, newID, proto, portName)
	}
	restored, subdomainDropped := s.restoreServerTail(ctx, newID, man)
	resp := map[string]any{"id": newID, "name": finalName, "status": "stopped"}
	if len(moved) > 0 {
		resp["ports_changed"] = moved // "game 25081→25000" — these need forwarding repointed
	}
	if len(restored) > 0 {
		resp["restored"] = restored // e.g. "2 schedules, 3 watchers"
	}
	if subdomainDropped != "" {
		resp["subdomain_dropped"] = subdomainDropped // already taken here — repoint by hand
	}
	return resp, nil
}

// serverNameTaken reports whether a server already uses this name (or its
// "(imported)" alias from an earlier move).
func (s *Server) serverNameTaken(ctx context.Context, name string) bool {
	var n int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM servers WHERE name=? OR name=?", name, name+" (imported)").Scan(&n)
	return n > 0
}

// pickTransferPorts maps each rune port to a host port, preferring the source's
// original port so a migrated server keeps its public game.host:PORT. A source
// port is honoured even when it sits outside this panel's auto-allocation range —
// preserving an explicit port is the whole point of a migration. When the source
// port is already in use here (or the source didn't carry one), it falls back to
// the first free port in [rangeMin,rangeMax]. `taken` is seeded with ports in use
// on the host and mutated as ports are claimed; `available` reports whether a
// single port is free ignoring `taken` (DB + live socket). Returns the assignment
// and a "name old→new" line for every port that had to move.
func pickTransferPorts(runePorts []gameskill.Port, source map[string]int, taken map[int]bool, rangeMin, rangeMax int, available func(int) bool) (map[string]int, []string, error) {
	if taken == nil {
		taken = map[int]bool{}
	}
	allocated := map[string]int{}
	var moved []string
	for _, p := range runePorts {
		if src := source[p.Name]; src > 0 && !taken[src] && available(src) {
			allocated[p.Name] = src
			taken[src] = true
			continue
		}
		got := 0
		for port := rangeMin; port <= rangeMax; port++ {
			if taken[port] || !available(port) {
				continue
			}
			got = port
			break
		}
		if got == 0 {
			return nil, nil, fmt.Errorf("no free ports in range %d-%d", rangeMin, rangeMax)
		}
		allocated[p.Name] = got
		taken[got] = true
		if src := source[p.Name]; src > 0 && src != got {
			moved = append(moved, fmt.Sprintf("%s %d→%d", p.Name, src, got))
		}
	}
	return allocated, moved, nil
}

// safeFilename reduces a server name to a filesystem-safe download filename.
func safeFilename(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "server"
	}
	return b.String()
}

// tarDataDir walks a data directory into the tar under the given prefix.
func tarDataDir(tw *tar.Writer, root, prefix string) {
	filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries rather than abort the export
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return nil
		}
		name := prefix + filepath.ToSlash(rel)
		link := ""
		if fi.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(path)
		}
		hdr, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return nil
		}
		hdr.Name = name
		if tw.WriteHeader(hdr) != nil {
			return nil
		}
		if fi.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()
			io.Copy(tw, f)
		}
		return nil
	})
}

// collectServerTail fills the v2 manifest fields: the server's schedules,
// watchers and notification channels (config decrypted — the bundle is already
// a credential by virtue of the env secrets).
func (s *Server) collectServerTail(ctx context.Context, serverID string, man *transferManifest) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT name, cron_expr, action, COALESCE(args_json,'{}'), enabled FROM schedules WHERE server_id=?", serverID)
	if err == nil {
		for rows.Next() {
			var t transferSchedule
			if rows.Scan(&t.Name, &t.CronExpr, &t.Action, &t.ArgsJSON, &t.Enabled) == nil {
				man.Schedules = append(man.Schedules, t)
			}
		}
		rows.Close()
	}
	rows, err = s.db.QueryContext(ctx,
		"SELECT name, pattern, threshold, window_secs, action, enabled, COALESCE(source,'') FROM log_watchers WHERE server_id=?", serverID)
	if err == nil {
		for rows.Next() {
			var t transferWatcher
			if rows.Scan(&t.Name, &t.Pattern, &t.Threshold, &t.WindowSecs, &t.Action, &t.Enabled, &t.Source) == nil {
				man.Watchers = append(man.Watchers, t)
			}
		}
		rows.Close()
	}
	rows, err = s.db.QueryContext(ctx,
		"SELECT type, config_enc, enabled FROM notifications WHERE server_id=?", serverID)
	if err == nil {
		for rows.Next() {
			var t transferChannel
			if rows.Scan(&t.Type, &t.Config, &t.Enabled) == nil {
				if plain, derr := s.cipher.Decrypt(t.Config); derr == nil {
					t.Config = plain
				}
				man.Channels = append(man.Channels, t)
			}
		}
		rows.Close()
	}
}

// restoreServerTail recreates the v2 tail on the imported server, re-encrypting
// channel configs with this panel's key. The subdomain is kept only when free
// here; a clash reports it back rather than silently stealing the route.
func (s *Server) restoreServerTail(ctx context.Context, serverID string, man *transferManifest) ([]string, string) {
	var restored []string
	for _, t := range man.Schedules {
		s.db.ExecContext(ctx,
			"INSERT INTO schedules (id, name, server_id, cron_expr, action, args_json, enabled) VALUES (?,?,?,?,?,?,?)",
			uuid.New().String(), t.Name, serverID, t.CronExpr, t.Action, t.ArgsJSON, t.Enabled)
	}
	if n := len(man.Schedules); n > 0 {
		restored = append(restored, fmt.Sprintf("%d schedules", n))
	}
	for _, t := range man.Watchers {
		s.db.ExecContext(ctx,
			"INSERT INTO log_watchers (id, server_id, name, pattern, threshold, window_secs, action, enabled, source) VALUES (?,?,?,?,?,?,?,?,?)",
			uuid.New().String(), serverID, t.Name, t.Pattern, t.Threshold, t.WindowSecs, t.Action, t.Enabled, t.Source)
	}
	if n := len(man.Watchers); n > 0 {
		restored = append(restored, fmt.Sprintf("%d watchers", n))
	}
	for _, t := range man.Channels {
		enc, err := s.cipher.Encrypt(t.Config)
		if err != nil {
			continue
		}
		s.db.ExecContext(ctx,
			"INSERT INTO notifications (id, type, config_enc, enabled, server_id) VALUES (?,?,?,?,?)",
			uuid.New().String(), t.Type, enc, t.Enabled, serverID)
	}
	if n := len(man.Channels); n > 0 {
		restored = append(restored, fmt.Sprintf("%d notification channels", n))
	}
	subdomainDropped := ""
	if man.Subdomain != "" {
		var taken int
		s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM servers WHERE subdomain=? AND id<>?", man.Subdomain, serverID).Scan(&taken)
		if taken == 0 {
			s.db.ExecContext(ctx, "UPDATE servers SET subdomain=? WHERE id=?", man.Subdomain, serverID)
			restored = append(restored, "subdomain "+man.Subdomain)
		} else {
			subdomainDropped = man.Subdomain
		}
	}
	return restored, subdomainDropped
}
