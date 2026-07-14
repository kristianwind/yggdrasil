package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/backup"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// ---- Backup targets (global-admin config) ----

type targetView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Path     string `json:"path"`
	Host     string `json:"host,omitempty"`
	KeepN    int    `json:"keep_n"`
	KeepDays int    `json:"keep_days"`
}

func (s *Server) handleListBackupTargets(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, name, type, config_enc, keep_n, keep_days FROM backup_targets ORDER BY name")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := []targetView{}
	for rows.Next() {
		var id, name, typ, enc string
		var keepN, keepDays int
		if err := rows.Scan(&id, &name, &typ, &enc, &keepN, &keepDays); err != nil {
			continue
		}
		// Decrypt only to surface non-secret fields (path/host); never password.
		cfg, _ := s.decryptTargetConfig(enc)
		list = append(list, targetView{
			ID: id, Name: name, Type: typ, Path: cfg.Path, Host: cfg.Host,
			KeepN: keepN, KeepDays: keepDays,
		})
	}
	jsonOK(w, list)
}

func (s *Server) handleCreateBackupTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		backup.Config
		KeepN    int `json:"keep_n"`
		KeepDays int `json:"keep_days"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Name == "" || req.Type == "" {
		jsonError(w, "name and type required", http.StatusBadRequest)
		return
	}
	enc, err := s.encryptTargetConfig(req.Config)
	if err != nil {
		jsonError(w, "encrypt: "+err.Error(), http.StatusInternalServerError)
		return
	}
	id := uuid.New().String()
	_, err = s.db.ExecContext(r.Context(),
		"INSERT INTO backup_targets (id, name, type, config_enc, keep_n, keep_days) VALUES (?,?,?,?,?,?)",
		id, req.Name, req.Type, enc, req.KeepN, req.KeepDays)
	if err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "backup_target.create", "target:"+id, map[string]string{"name": req.Name, "type": req.Type})
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{"id": id})
}

func (s *Server) handleDeleteBackupTarget(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.db.ExecContext(r.Context(), "DELETE FROM backup_targets WHERE id=?", id); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "backup_target.delete", "target:"+id, nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}

func (s *Server) handleTestBackupTarget(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cfg, err := s.loadTargetConfig(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	tgt, err := backup.Open(*cfg)
	if err != nil {
		jsonError(w, "connect failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer tgt.Close()
	if _, err := tgt.List(r.Context()); err != nil {
		jsonError(w, "list failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]string{"status": "ok"})
}

// ---- Backups (per-server) ----

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerBackup, s.serverTarget(r.Context(), id)) {
		return
	}
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, COALESCE(target_id,''), COALESCE(path,''), COALESCE(size_bytes,0),
		        status, COALESCE(error_msg,''), created_at, COALESCE(completed_at,''),
		        COALESCE(verified_at,''), COALESCE(verify_ok,-1)
		 FROM backups WHERE server_id=? ORDER BY created_at DESC`, id)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type bk struct {
		ID          string `json:"id"`
		TargetID    string `json:"target_id"`
		Path        string `json:"path"`
		Size        int64  `json:"size_bytes"`
		Status      string `json:"status"`
		Error       string `json:"error,omitempty"`
		CreatedAt   string `json:"created_at"`
		CompletedAt string `json:"completed_at,omitempty"`
		VerifiedAt  string `json:"verified_at,omitempty"`
		VerifyOK    int    `json:"verify_ok"` // -1 unknown, 0 corrupt, 1 ok
	}
	list := []bk{}
	for rows.Next() {
		var b bk
		if err := rows.Scan(&b.ID, &b.TargetID, &b.Path, &b.Size, &b.Status, &b.Error, &b.CreatedAt, &b.CompletedAt, &b.VerifiedAt, &b.VerifyOK); err != nil {
			continue
		}
		list = append(list, b)
	}
	jsonOK(w, list)
}

func (s *Server) handleRunBackup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerBackup, s.serverTarget(r.Context(), id)) {
		return
	}
	var req struct {
		TargetID string `json:"target_id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.TargetID == "" {
		jsonError(w, "target_id required", http.StatusBadRequest)
		return
	}
	backupID := uuid.New().String()
	_, err := s.db.ExecContext(r.Context(),
		"INSERT INTO backups (id, server_id, target_id, status) VALUES (?,?,?,'pending')",
		backupID, id, req.TargetID)
	if err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "backup.run", "server:"+id, map[string]string{"backup": backupID})
	go s.runBackup(id, req.TargetID, backupID) //nolint:errcheck
	w.WriteHeader(http.StatusAccepted)
	jsonOK(w, map[string]string{"id": backupID, "status": "pending"})
}

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var serverID, targetID, path string
	err := s.db.QueryRowContext(r.Context(),
		"SELECT server_id, COALESCE(target_id,''), COALESCE(path,'') FROM backups WHERE id=?", id).
		Scan(&serverID, &targetID, &path)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerBackup, s.serverTarget(r.Context(), serverID)) {
		return
	}
	if targetID != "" && path != "" {
		if cfg, err := s.loadTargetConfig(r.Context(), targetID); err == nil {
			if tgt, err := backup.Open(*cfg); err == nil {
				tgt.Delete(r.Context(), path)
				tgt.Close()
			}
		}
	}
	s.db.ExecContext(r.Context(), "DELETE FROM backups WHERE id=?", id)
	s.auditLog(r, "backup.delete", "backup:"+id, nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var serverID, targetID, path, dataDir, containerID string
	err := s.db.QueryRowContext(r.Context(),
		`SELECT b.server_id, COALESCE(b.target_id,''), COALESCE(b.path,''), s.data_dir, COALESCE(s.container_id,'')
		 FROM backups b JOIN servers s ON s.id=b.server_id WHERE b.id=?`, id).
		Scan(&serverID, &targetID, &path, &dataDir, &containerID)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerBackup, s.serverTarget(r.Context(), serverID)) {
		return
	}
	cfg, err := s.loadTargetConfig(r.Context(), targetID)
	if err != nil {
		jsonError(w, "target unavailable", http.StatusBadGateway)
		return
	}
	tgt, err := backup.Open(*cfg)
	if err != nil {
		jsonError(w, "connect: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer tgt.Close()

	// Stop the container first so files aren't overwritten under a running game.
	if containerID != "" {
		s.docker.Stop(r.Context(), containerID, 30)
		s.db.ExecContext(r.Context(), "UPDATE servers SET status='stopped' WHERE id=?", serverID)
	}

	rc, err := tgt.Get(r.Context(), path)
	if err != nil {
		jsonError(w, "fetch backup: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()
	if err := backup.Restore(rc, dataDir); err != nil {
		jsonError(w, "restore: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "backup.restore", "server:"+serverID, map[string]string{"backup": id})
	jsonOK(w, map[string]string{"status": "restored"})
}

type verifyResult struct {
	OK      bool   `json:"ok"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
	Archive int64  `json:"archive_bytes"`
	Error   string `json:"error,omitempty"`
}

// verifyBackupByID fetches a backup, streams it through gzip+tar to confirm it
// decompresses in full, and persists the outcome (verify_ok + verified_at). Shared
// by the manual "Verify" button and the nightly auto-verify loop. A corrupt archive
// yields ok=false (not a transport error); err is only for fetch/target failures.
func (s *Server) verifyBackupByID(ctx context.Context, id string) (verifyResult, error) {
	var targetID, path string
	var archiveBytes int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(target_id,''), COALESCE(path,''), COALESCE(size_bytes,0) FROM backups WHERE id=?`, id).
		Scan(&targetID, &path, &archiveBytes); err != nil {
		return verifyResult{}, fmt.Errorf("not found")
	}
	cfg, err := s.loadTargetConfig(ctx, targetID)
	if err != nil {
		return verifyResult{}, fmt.Errorf("target unavailable")
	}
	tgt, err := backup.Open(*cfg)
	if err != nil {
		return verifyResult{}, fmt.Errorf("connect: %w", err)
	}
	defer tgt.Close()
	rc, err := tgt.Get(ctx, path)
	if err != nil {
		return verifyResult{}, fmt.Errorf("fetch backup: %w", err)
	}
	defer rc.Close()

	entries, total, verr := backup.Verify(rc)
	res := verifyResult{OK: verr == nil, Files: entries, Bytes: total, Archive: archiveBytes}
	okInt := 1
	if verr != nil {
		res.Error = verr.Error()
		okInt = 0
	}
	s.db.ExecContext(ctx, "UPDATE backups SET verified_at=datetime('now'), verify_ok=? WHERE id=?", okInt, id)
	return res, nil
}

// handleVerifyBackup checks one backup's integrity on demand.
func (s *Server) handleVerifyBackup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var serverID string
	if err := s.db.QueryRowContext(r.Context(), "SELECT server_id FROM backups WHERE id=?", id).Scan(&serverID); err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerBackup, s.serverTarget(r.Context(), serverID)) {
		return
	}
	res, err := s.verifyBackupByID(r.Context(), id)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.auditLog(r, "backup.verify", "server:"+serverID, map[string]any{"backup": id, "ok": res.OK})
	jsonOK(w, res)
}

// ---- Manager ----

// runBackup archives a server and uploads it to the target, then applies the
// target's retention policy. Status is recorded on the backups row.
func (s *Server) runBackup(serverID, targetID, backupID string) {
	defer recoverLog("runBackup")
	ctx := context.Background()
	fail := func(msg string) {
		s.db.Exec("UPDATE backups SET status='error', error_msg=?, completed_at=? WHERE id=?",
			msg, time.Now().UTC().Format(time.RFC3339), backupID)
		s.notifyAll("❌ Backup failed for " + s.serverName(serverID) + ": " + msg)
	}
	s.db.Exec("UPDATE backups SET status='running' WHERE id=?", backupID)

	var dataDir, gameskillID string
	if err := s.db.QueryRow("SELECT data_dir, gameskill_id FROM servers WHERE id=?", serverID).
		Scan(&dataDir, &gameskillID); err != nil {
		fail("server not found")
		return
	}
	var include []string
	var yamlBlob string
	if s.db.QueryRow("SELECT yaml_blob FROM gameskills WHERE id=?", gameskillID).Scan(&yamlBlob) == nil {
		if gs, err := gameskill.Parse([]byte(yamlBlob)); err == nil && gs.Backup != nil {
			include = gs.Backup.Include
		}
	}

	cfg, err := s.loadTargetConfig(ctx, targetID)
	if err != nil {
		fail("target config: " + err.Error())
		return
	}
	tgt, err := backup.Open(*cfg)
	if err != nil {
		fail("connect: " + err.Error())
		return
	}
	defer tgt.Close()

	// Store under a human-readable folder ("<server-name>-<short-id>") instead of a
	// bare UUID so backups are identifiable when browsing the target (NAS/SFTP/SMB).
	// The short id keeps it unique if two servers share a name; retention works off
	// the DB `path` column, not directory listing, so this layout is safe to change.
	short := serverID
	if len(short) > 8 {
		short = short[:8]
	}
	name := fmt.Sprintf("%s-%s/%s.tar.gz", slugName(s.serverName(serverID)), short, time.Now().UTC().Format("20060102-150405"))

	// Stream the archive straight to the target.
	pr, pw := io.Pipe()
	go func() {
		err := backup.Archive(dataDir, include, pw)
		pw.CloseWithError(err)
	}()
	size, err := tgt.Put(ctx, name, pr)
	if err != nil {
		fail("upload: " + err.Error())
		return
	}

	s.db.Exec("UPDATE backups SET status='done', path=?, size_bytes=?, completed_at=? WHERE id=?",
		name, size, time.Now().UTC().Format(time.RFC3339), backupID)
	s.notifyAll("✅ Backup complete for " + s.serverName(serverID) + " (" + humanBytes(size) + ")")

	s.applyRetention(ctx, serverID, targetID, tgt)
}

// slugName turns a server name into a filesystem-friendly token for backup paths
// (lowercase, alphanumerics kept, runs of anything else collapsed to a single dash).
func slugName(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "server"
	}
	return out
}

// applyRetention deletes backups beyond the target's keep-N / keep-days policy.
func (s *Server) applyRetention(ctx context.Context, serverID, targetID string, tgt backup.Target) {
	var keepN, keepDays int
	s.db.QueryRow("SELECT keep_n, keep_days FROM backup_targets WHERE id=?", targetID).Scan(&keepN, &keepDays)
	if keepN <= 0 && keepDays <= 0 {
		return
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, path, created_at FROM backups WHERE server_id=? AND target_id=? AND status='done'",
		serverID, targetID)
	if err != nil {
		return
	}
	type rec struct {
		id, path string
		obj      backup.Object
	}
	var recs []rec
	var objs []backup.Object
	for rows.Next() {
		var id, path, created string
		if rows.Scan(&id, &path, &created) != nil {
			continue
		}
		t, _ := time.Parse(time.RFC3339, created)
		o := backup.Object{Name: path, ModTime: t}
		recs = append(recs, rec{id, path, o})
		objs = append(objs, o)
	}
	rows.Close()

	del := backup.Retention(objs, keepN, keepDays, time.Now().UTC())
	delSet := map[string]bool{}
	for _, d := range del {
		delSet[d.Name] = true
	}
	for _, rc := range recs {
		if delSet[rc.path] {
			tgt.Delete(ctx, rc.path)
			s.db.Exec("DELETE FROM backups WHERE id=?", rc.id)
		}
	}
}

// ---- target config crypto ----

func (s *Server) encryptTargetConfig(cfg backup.Config) (string, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return s.cipher.Encrypt(string(b))
}

func (s *Server) decryptTargetConfig(enc string) (backup.Config, error) {
	var cfg backup.Config
	plain, err := s.cipher.Decrypt(enc)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal([]byte(plain), &cfg)
	return cfg, err
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func (s *Server) loadTargetConfig(ctx context.Context, targetID string) (*backup.Config, error) {
	var enc string
	if err := s.db.QueryRowContext(ctx, "SELECT config_enc FROM backup_targets WHERE id=?", targetID).Scan(&enc); err != nil {
		return nil, err
	}
	cfg, err := s.decryptTargetConfig(enc)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}
