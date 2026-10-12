package api

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kristianwind/yggdrasil/internal/backup"
	"github.com/kristianwind/yggdrasil/internal/panelbackup"
)

// Backing up the panel itself.
//
// Every server has backups; the thing that knows which servers exist, on which
// ports, behind which domains, with which credentials, had none. Restoring a
// host meant rebuilding that by hand from memory.
//
// The archive is the database plus auth.secret_key, encrypted with a passphrase
// (see internal/panelbackup for why it must contain the key, and therefore why
// it must be encrypted). Server data directories are not in it: they have their
// own backups, and a 16 GB job does not run every night.

const (
	panelBackupPrefix = "panel/"
	panelBackupSuffix = ".yggpanel.enc"
	panelBackupScan   = 15 * time.Minute
)

func (s *Server) startPanelBackupLoop() {
	go func() {
		defer recoverLog("panelBackupLoop")
		t := time.NewTicker(panelBackupScan)
		defer t.Stop()
		for range t.C {
			s.maybeRunPanelBackup()
		}
	}()
}

// maybeRunPanelBackup runs one backup a day, at the configured hour. The last
// success is stored as a DATE rather than a timestamp, so a panel restarted
// three times inside the hour makes one archive and not three.
func (s *Server) maybeRunPanelBackup() {
	defer recoverLog("maybeRunPanelBackup")
	ctx := context.Background()
	if s.getSetting(ctx, "panel_backup_enabled") != "1" {
		return
	}
	hour, _ := strconv.Atoi(s.getSetting(ctx, "panel_backup_hour"))
	now := time.Now()
	if now.Hour() != hour {
		return
	}
	today := now.Format("2006-01-02")
	if s.getSetting(ctx, "panel_backup_last_day") == today {
		return
	}
	s.setSetting(ctx, "panel_backup_last_day", today)
	if _, err := s.runPanelBackup(ctx); err != nil {
		log.Printf("panel backup: %v", err)
	}
}

// runPanelBackup makes one archive and ships it to the configured target.
func (s *Server) runPanelBackup(ctx context.Context) (string, error) {
	targetID := s.getSetting(ctx, "panel_backup_target_id")
	if targetID == "" {
		return "", fmt.Errorf("no backup target chosen for the panel backup")
	}
	encPass := s.getSetting(ctx, "panel_backup_pass")
	if encPass == "" {
		return "", fmt.Errorf("no passphrase set: the archive carries the panel's encryption key and will not be written without one")
	}
	pass, err := s.cipher.Decrypt(encPass)
	if err != nil {
		return "", fmt.Errorf("decrypt the backup passphrase: %w", err)
	}

	id := uuid.New().String()
	// The timestamp has second resolution, so two runs in the same second would
	// land on one name and the second would silently overwrite the first --
	// "run now" pressed twice, or a retry after a transient upload failure. The
	// id keeps them apart while the timestamp keeps the list sortable, which is
	// what retention orders by.
	name := panelBackupPrefix + time.Now().UTC().Format("20060102-150405") + "-" + id[:8] + panelBackupSuffix
	s.db.ExecContext(ctx, //nolint:errcheck
		"INSERT INTO panel_backups (id, target_id, path, status) VALUES (?,?,?,'running')", id, targetID, name)

	fail := func(e error) (string, error) {
		s.db.ExecContext(ctx, //nolint:errcheck
			"UPDATE panel_backups SET status='error', error_msg=?, completed_at=? WHERE id=?",
			e.Error(), time.Now().UTC().Format(time.RFC3339), id)
		// Said out loud. A panel backup that quietly stops is worth less than no
		// panel backup at all, because the operator believes they have one.
		s.notifyAll("⚠️ The panel's own backup failed: " + e.Error())
		s.auditSystem("panel_backup.failed", "panel", "yggdrasil", map[string]any{"error": e.Error()})
		return "", e
	}

	cfg, err := s.loadTargetConfig(ctx, targetID)
	if err != nil {
		return fail(fmt.Errorf("target config: %w", err))
	}
	if cfg.Type == backup.PBSType {
		// PBS stores deduplicated chunks of a directory, not a named blob; handing
		// it one archive dedupes against nothing and the operator would never know.
		return fail(fmt.Errorf("a Proxmox Backup Server target cannot hold the panel archive — choose a file target (local, NFS, SMB or SFTP)"))
	}
	tgt, err := backup.Open(*cfg)
	if err != nil {
		return fail(fmt.Errorf("connect: %w", err))
	}
	defer tgt.Close()

	var buf bytes.Buffer
	if err := panelbackup.Create(s.cfg, s.db, pass, &buf); err != nil {
		return fail(err)
	}
	size, err := tgt.Put(ctx, name, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return fail(fmt.Errorf("upload: %w", err))
	}

	s.db.ExecContext(ctx, //nolint:errcheck
		"UPDATE panel_backups SET status='done', size_bytes=?, completed_at=? WHERE id=?",
		size, time.Now().UTC().Format(time.RFC3339), id)
	s.auditSystem("panel_backup.done", "panel", "yggdrasil", map[string]any{"path": name, "bytes": size})
	s.applyPanelRetention(ctx, targetID, tgt)
	return name, nil
}

// applyPanelRetention keeps the newest N archives and deletes the rest.
//
// Driven by the database, not by listing the target, and that is not a style
// choice. A target's List reads one directory and skips subdirectories, while
// these archives live under panel/ — so a listing-based policy finds nothing,
// deletes nothing, and the disk fills with a retention setting that reads as
// applied. (Measured: four archives, keep=2, none removed.) Server backups
// already work this way for the same reason.
//
// It also makes "never touch anything that is not ours" true by construction
// rather than by a prefix match: the only paths considered are the ones this
// panel recorded writing.
func (s *Server) applyPanelRetention(ctx context.Context, targetID string, tgt backup.Target) {
	keep, _ := strconv.Atoi(s.getSetting(ctx, "panel_backup_keep"))
	if keep <= 0 {
		return // no policy: keep everything
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, path FROM panel_backups
		 WHERE target_id=? AND status='done' AND COALESCE(path,'')<>''
		 ORDER BY created_at DESC, path DESC`, targetID)
	if err != nil {
		log.Printf("panel backup: could not read history for retention: %v", err)
		return
	}
	type rec struct{ id, path string }
	var recs []rec
	for rows.Next() {
		var r rec
		if rows.Scan(&r.id, &r.path) == nil {
			recs = append(recs, r)
		}
	}
	rows.Close()
	if len(recs) <= keep {
		return
	}
	for _, r := range recs[keep:] {
		if err := tgt.Delete(ctx, r.path); err != nil {
			// Leave the row: a file we could not delete is still out there, and
			// forgetting it is how it becomes nobody's to clean up.
			log.Printf("panel backup: could not delete %s: %v", r.path, err)
			continue
		}
		s.db.ExecContext(ctx, "DELETE FROM panel_backups WHERE id=?", r.id) //nolint:errcheck
	}
}

// --- HTTP ---

func (s *Server) handleGetPanelBackupSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hour, _ := strconv.Atoi(s.getSetting(ctx, "panel_backup_hour"))
	keep, _ := strconv.Atoi(s.getSetting(ctx, "panel_backup_keep"))
	jsonOK(w, map[string]any{
		"enabled":   s.getSetting(ctx, "panel_backup_enabled") == "1",
		"target_id": s.getSetting(ctx, "panel_backup_target_id"),
		"hour":      hour,
		"keep":      keep,
		// Never the passphrase itself — only whether one is set. It is the one
		// thing standing between the backup target and every stored credential.
		"passphrase_set": s.getSetting(ctx, "panel_backup_pass") != "",
		"last_day":       s.getSetting(ctx, "panel_backup_last_day"),
	})
}

func (s *Server) handleSetPanelBackupSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled    bool   `json:"enabled"`
		TargetID   string `json:"target_id"`
		Hour       int    `json:"hour"`
		Keep       int    `json:"keep"`
		Passphrase string `json:"passphrase"` // blank = keep the existing one
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if req.Passphrase != "" {
		enc, err := s.cipher.Encrypt(req.Passphrase)
		if err != nil {
			jsonError(w, "could not store the passphrase", http.StatusInternalServerError)
			return
		}
		s.setSetting(ctx, "panel_backup_pass", enc)
	}
	// Turning it on without a passphrase would schedule a job that can only fail,
	// every night, and report it. Refuse while it can still be fixed in front of
	// the person who caused it.
	if req.Enabled && s.getSetting(ctx, "panel_backup_pass") == "" {
		jsonError(w, "set a passphrase first — the archive carries the panel's encryption key and is useless unencrypted", http.StatusBadRequest)
		return
	}
	if req.Enabled && strings.TrimSpace(req.TargetID) == "" {
		jsonError(w, "choose where the backup should be written", http.StatusBadRequest)
		return
	}
	if req.Hour < 0 || req.Hour > 23 {
		jsonError(w, "hour must be between 0 and 23", http.StatusBadRequest)
		return
	}
	s.setSetting(ctx, "panel_backup_enabled", boolStr(req.Enabled))
	s.setSetting(ctx, "panel_backup_target_id", strings.TrimSpace(req.TargetID))
	s.setSetting(ctx, "panel_backup_hour", strconv.Itoa(req.Hour))
	s.setSetting(ctx, "panel_backup_keep", strconv.Itoa(req.Keep))
	s.auditLog(r, "settings.panel_backup", "panel", map[string]any{
		"enabled": req.Enabled, "hour": req.Hour, "keep": req.Keep,
	})
	s.handleGetPanelBackupSettings(w, r)
}

func (s *Server) handleRunPanelBackup(w http.ResponseWriter, r *http.Request) {
	name, err := s.runPanelBackup(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.auditLog(r, "panel_backup.run", "panel", map[string]any{"path": name})
	jsonOK(w, map[string]string{"status": "done", "path": name})
}

func (s *Server) handleListPanelBackups(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, COALESCE(target_id,''), COALESCE(path,''), COALESCE(size_bytes,0),
		        status, COALESCE(error_msg,''), created_at, COALESCE(completed_at,'')
		 FROM panel_backups ORDER BY created_at DESC LIMIT 50`)
	if err != nil {
		jsonError(w, "list: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, tid, path, status, errMsg, created, completed string
		var size int64
		if rows.Scan(&id, &tid, &path, &size, &status, &errMsg, &created, &completed) != nil {
			continue
		}
		out = append(out, map[string]any{
			"id": id, "target_id": tid, "path": path, "size_bytes": size,
			"status": status, "error_msg": errMsg, "created_at": created, "completed_at": completed,
		})
	}
	jsonOK(w, out)
}
