package api

import (
	"archive/tar"
	"compress/gzip"
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

const transferVersion = 1

// transferManifest is a single server's portable setup: enough to recreate it on
// another panel, plus the rune so the target doesn't need it pre-installed. Env
// secrets are DECRYPTED here (the source re-encrypts nothing; the target
// re-encrypts with its own key), which makes the bundle a credential — admin-only
// on both ends, and it should be handled like one.
type transferManifest struct {
	Version     int               `json:"version"`
	Name        string            `json:"name"`
	GameskillID string            `json:"gameskill_id"`
	RuneYAML    string            `json:"rune_yaml"`
	Env         map[string]string `json:"env"`
	CPULimit    float64           `json:"cpu_limit"`
	MemLimitMB  int64             `json:"mem_limit_mb"`
	HostMounts  string            `json:"host_mounts"`
	Autostart   int               `json:"autostart"`
	AutoForward int               `json:"auto_forward"`
	Watchdog    int               `json:"watchdog"`
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

	var envJSON, hostMounts, yamlBlob string
	var cpu float64
	var mem int64
	var autostart, autoForward, watchdog int
	if err := s.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(s.env_json,'{}'), COALESCE(s.host_mounts,''), COALESCE(s.cpu_limit,0), COALESCE(s.mem_limit_mb,0),
		       COALESCE(s.autostart,1), COALESCE(s.auto_forward,1), COALESCE(s.watchdog,0), g.yaml_blob
		FROM servers s JOIN gameskills g ON g.id=s.gameskill_id WHERE s.id=?`, id).
		Scan(&envJSON, &hostMounts, &cpu, &mem, &autostart, &autoForward, &watchdog, &yamlBlob); err != nil {
		jsonError(w, "source read failed", http.StatusInternalServerError)
		return
	}
	gs, err := gameskill.Parse([]byte(yamlBlob))
	if err != nil {
		jsonError(w, "gameskill parse error", http.StatusInternalServerError)
		return
	}
	env := map[string]string{}
	json.Unmarshal([]byte(envJSON), &env)
	s.decryptSecretEnv(env, gs) // plaintext into the bundle; the target re-encrypts

	man := transferManifest{
		Version: transferVersion, Name: src.Name, GameskillID: src.GameskillID, RuneYAML: yamlBlob,
		Env: env, CPULimit: cpu, MemLimitMB: mem, HostMounts: hostMounts,
		Autostart: autostart, AutoForward: autoForward, Watchdog: watchdog,
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", safeFilename(src.Name)+".yggserver.tar.gz"))
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	manJSON, _ := json.MarshalIndent(man, "", "  ")
	tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o644, Size: int64(len(manJSON))})
	tw.Write(manJSON)
	if src.DataDir != "" {
		if fi, err := os.Stat(src.DataDir); err == nil && fi.IsDir() {
			tarDataDir(tw, src.DataDir) // best-effort; a read error just truncates the data
		}
	}
	tw.Close()
	gz.Close()
	s.auditLog(r, "server.export", "server:"+id, map[string]string{"name": src.Name})
}

// handleServerImport creates a new server on this panel from an exported bundle:
// adds the rune if missing, re-encrypts the secrets with this panel's key,
// allocates fresh ports, restores the data directory, and marks it installed.
// Admin-only.
func (s *Server) handleServerImport(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	gz, err := gzip.NewReader(io.LimitReader(r.Body, 8<<30))
	if err != nil {
		jsonError(w, "not a valid bundle", http.StatusBadRequest)
		return
	}
	tr := tar.NewReader(gz)

	newID := uuid.New().String()
	base := s.cfg.Database.Path[:len(s.cfg.Database.Path)-len(filepath.Base(s.cfg.Database.Path))]
	dataDir := filepath.Join(base, "servers", newID)

	var man *transferManifest
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			jsonError(w, "corrupt bundle", http.StatusBadRequest)
			return
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			jsonError(w, "unsafe bundle entry", http.StatusBadRequest)
			return
		}
		if name == "manifest.json" {
			b, _ := io.ReadAll(tr)
			var m transferManifest
			if json.Unmarshal(b, &m) != nil || m.Version != transferVersion {
				jsonError(w, "unsupported bundle version", http.StatusBadRequest)
				return
			}
			man = &m
			continue
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
		jsonError(w, "no manifest in bundle", http.StatusBadRequest)
		return
	}

	// Ensure the rune exists — add it (as a community rune) when the target panel
	// doesn't already have it.
	gs, err := gameskill.Parse([]byte(man.RuneYAML))
	if err != nil {
		os.RemoveAll(dataDir)
		jsonError(w, "bundle rune parse error", http.StatusBadRequest)
		return
	}
	var have int
	s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM gameskills WHERE id=?", man.GameskillID).Scan(&have)
	if have == 0 {
		s.db.ExecContext(r.Context(),
			"INSERT INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES (?,?,?,?,?,0)",
			gs.ID, gs.Name, gs.Category, gs.Version, man.RuneYAML)
	} else {
		// Parse the panel's existing copy — its ports/secret-keys are what we honour.
		var yb string
		s.db.QueryRowContext(r.Context(), "SELECT yaml_blob FROM gameskills WHERE id=?", man.GameskillID).Scan(&yb)
		if g2, e := gameskill.Parse([]byte(yb)); e == nil {
			gs = g2
		}
	}

	// Fresh ports on this panel.
	allocated := map[string]int{}
	taken, _ := s.docker.UsedHostPorts(r.Context())
	if taken == nil {
		taken = map[int]bool{}
	}
	for _, p := range gs.Ports {
		hp, err := s.allocatePort(r.Context(), p.Default, taken)
		if err != nil {
			os.RemoveAll(dataDir)
			jsonError(w, "port allocation failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		allocated[p.Name] = hp
		taken[hp] = true
	}

	// Re-encrypt secrets with THIS panel's key.
	env := man.Env
	if env == nil {
		env = map[string]string{}
	}
	s.encryptSecretEnv(env, gs)
	envJSON, _ := json.Marshal(env)
	portsJSON, _ := json.Marshal(allocated)

	if _, err := s.db.ExecContext(r.Context(), `
		INSERT INTO servers (id, name, gameskill_id, status, env_json, ports_json, cpu_limit, mem_limit_mb,
		                     data_dir, host_mounts, autostart, auto_forward, watchdog, installed, install_status)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,1,'done')`,
		newID, man.Name+" (imported)", man.GameskillID, "stopped", string(envJSON), string(portsJSON),
		man.CPULimit, man.MemLimitMB, dataDir, man.HostMounts, man.Autostart, man.AutoForward, man.Watchdog); err != nil {
		os.RemoveAll(dataDir)
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
		s.db.ExecContext(r.Context(), "INSERT INTO port_allocations (port, server_id, protocol, name) VALUES (?,?,?,?)",
			hostPort, newID, proto, portName)
	}
	s.auditLog(r, "server.import", "server:"+newID, map[string]string{"name": man.Name})
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{"id": newID, "name": man.Name + " (imported)", "status": "stopped"})
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

// tarDataDir walks a data directory into the tar under data/.
func tarDataDir(tw *tar.Writer, root string) {
	filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries rather than abort the export
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return nil
		}
		name := "data/" + filepath.ToSlash(rel)
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
