package api

import (
	"context"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// warnedRestart broadcasts the rune's restart countdown to players, optionally
// takes a safety backup, then recreates the server. It BLOCKS for the countdown
// duration, so callers run it in a goroutine. With no warnings defined it just
// (optionally) backs up and restarts immediately.
func (s *Server) warnedRestart(serverID string, backupFirst bool, targetID string) {
	ctx := context.Background()
	rt, err := s.loadRuntime(ctx, serverID)
	if err != nil {
		return
	}
	if backupFirst && targetID != "" {
		backupID := uuid.New().String()
		if _, err := s.db.Exec(
			"INSERT INTO backups (id, server_id, target_id, status) VALUES (?,?,?,'pending')",
			backupID, serverID, targetID); err == nil {
			// Unlike a wipe, a failed backup doesn't abort here: the restart is
			// what was asked for and it destroys nothing. runBackup has already
			// sent the ❌ notification; log it so the reason is on the host too.
			if err := s.runBackup(serverID, targetID, backupID); err != nil {
				log.Printf("safe restart: backup before restarting %s failed, restarting anyway: %v",
					s.serverName(serverID), err)
			}
		}
	}
	// Countdown: send each warning from the largest 'at' down to zero, sleeping
	// the gap to the next one so a [15m,5m,1m] set warns 15, then 5, then 1 min
	// before the restart.
	type warn struct {
		at  time.Duration
		msg string
	}
	var ws []warn
	if rt.gs.Restart != nil {
		for _, rw := range rt.gs.Restart.Warnings {
			if d, err := time.ParseDuration(rw.At); err == nil && d > 0 {
				ws = append(ws, warn{d, rw.Msg})
			}
		}
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].at > ws[j].at })
	for i, w := range ws {
		if w.msg != "" {
			s.sendToServer(serverID, w.msg)
		}
		next := time.Duration(0)
		if i+1 < len(ws) {
			next = ws[i+1].at
		}
		time.Sleep(w.at - next)
	}
	s.recreateAndStart(ctx, serverID)
}

func (s *Server) handleSafeRestart(w http.ResponseWriter, r *http.Request) {
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
		BackupFirst bool   `json:"backup_first"`
		TargetID    string `json:"target_id"`
	}
	decodeJSON(r, &req)
	go s.warnedRestart(id, req.BackupFirst, req.TargetID)
	s.auditLog(r, "server.safe_restart", "server:"+id, nil)
	jsonOK(w, map[string]string{"status": "restart scheduled with warnings"})
}
