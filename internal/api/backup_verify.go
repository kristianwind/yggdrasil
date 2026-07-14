package api

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Nightly backup verification: once a day (when enabled) the panel integrity-checks
// each server's most recent backup and raises an alert if one is corrupt — so a
// dead backup is caught proactively, not discovered mid-restore. Off by default
// because it downloads each archive from its target (bandwidth on remote SFTP/SMB
// stores); it only touches the latest backup per server to keep that bounded.

const backupVerifyInterval = 30 * time.Minute

func (s *Server) startBackupVerifyLoop() {
	go func() {
		defer recoverLog("backupVerifyLoop")
		t := time.NewTicker(backupVerifyInterval)
		defer t.Stop()
		for range t.C {
			s.maybeAutoVerifyBackups()
		}
	}()
}

// maybeAutoVerifyBackups runs the daily pass at most once per day (with catch-up),
// verifying the newest completed backup of every server and notifying on any
// corruption.
func (s *Server) maybeAutoVerifyBackups() {
	defer recoverLog("maybeAutoVerifyBackups")
	ctx := context.Background()
	if s.getSetting(ctx, "backup_verify_enabled") != "1" {
		return
	}
	today := time.Now().UTC().Format("2006-01-02")
	if s.getSetting(ctx, "backup_verify_last_day") == today {
		return
	}
	// Stamp the day up front so a slow pass can't re-trigger on the next tick.
	s.setSetting(ctx, "backup_verify_last_day", today)

	rows, err := s.db.Query(`
		SELECT b.id, b.server_id FROM backups b
		JOIN (SELECT server_id, MAX(created_at) mc FROM backups WHERE status='done' GROUP BY server_id) latest
		  ON latest.server_id = b.server_id AND latest.mc = b.created_at
		WHERE b.status='done'`)
	if err != nil {
		return
	}
	type bk struct{ id, serverID string }
	var list []bk
	for rows.Next() {
		var x bk
		if rows.Scan(&x.id, &x.serverID) == nil {
			list = append(list, x)
		}
	}
	rows.Close()

	var corrupt []string
	for _, x := range list {
		res, err := s.verifyBackupByID(ctx, x.id)
		if err != nil {
			continue // transport/target issue — not a corruption verdict, skip quietly
		}
		if !res.OK {
			corrupt = append(corrupt, fmt.Sprintf("%s (%s)", s.serverName(x.serverID), res.Error))
		}
	}
	if len(corrupt) > 0 {
		msg := "🛑 Backup verification found a corrupt latest backup:\n• " + corrupt[0]
		for _, c := range corrupt[1:] {
			msg += "\n• " + c
		}
		msg += "\nRun a fresh backup for the affected server(s)."
		s.notifyAll(msg)
	}
}

func (s *Server) handleGetBackupVerify(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"enabled":  s.getSetting(r.Context(), "backup_verify_enabled") == "1",
		"last_run": s.getSetting(r.Context(), "backup_verify_last_day"),
	})
}

func (s *Server) handleSetBackupVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Enabled != nil {
		s.setSetting(r.Context(), "backup_verify_enabled", boolStr(*req.Enabled))
	}
	s.auditLog(r, "settings.backup_verify", "backup_verify", map[string]any{"enabled": req.Enabled != nil && *req.Enabled})
	s.handleGetBackupVerify(w, r)
}
