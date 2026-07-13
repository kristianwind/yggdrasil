package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// wipeServer resets a server's persistence by deleting the rune's declared wipe
// paths (jailed to the data dir), optionally taking a safety backup first, then
// bringing the server back up if it was running. Reusable by the manual endpoint
// and the scheduler.
func (s *Server) wipeServer(ctx context.Context, id string, backupFirst bool, targetID string) error {
	srv, err := s.getServer(ctx, id)
	if err != nil {
		return err
	}
	rt, err := s.loadRuntime(ctx, id)
	if err != nil {
		return fmt.Errorf("load runtime: %w", err)
	}
	if rt.gs.Wipe == nil || len(rt.gs.Wipe.Paths) == 0 {
		return fmt.Errorf("this rune has no wipe definition")
	}
	wasRunning := srv.Status == "running"

	// 1. Safety backup first — abort the wipe if it fails, so persistence is never
	//    deleted without a recoverable copy.
	if backupFirst {
		if targetID == "" {
			return fmt.Errorf("backup-first requires a backup target")
		}
		backupID := uuid.New().String()
		if _, err := s.db.ExecContext(ctx,
			"INSERT INTO backups (id, server_id, target_id, status) VALUES (?,?,?,'pending')",
			backupID, id, targetID); err != nil {
			return fmt.Errorf("could not queue safety backup: %w", err)
		}
		s.runBackup(id, targetID, backupID) // run synchronously so we can gate on it
		var st string
		s.db.QueryRowContext(ctx, "SELECT status FROM backups WHERE id=?", backupID).Scan(&st)
		if st != "done" {
			return fmt.Errorf("safety backup did not complete (%s) — wipe aborted", st)
		}
	}

	// 2. Graceful stop + remove the container.
	if srv.ContainerID != "" {
		if rt.gs.Startup.Stop != "" {
			s.docker.SendStdin(ctx, srv.ContainerID, rt.gs.Startup.Stop)
			time.Sleep(3 * time.Second) // let the world save before we stop
		}
		s.docker.Stop(ctx, srv.ContainerID, 30)
		s.docker.Remove(ctx, srv.ContainerID)
		s.db.ExecContext(ctx, "UPDATE servers SET container_id='', status='stopped' WHERE id=?", id)
	}

	// 3. Delete the declared wipe paths, jailed to the data dir.
	if _, err := s.wipePaths(srv.DataDir, rt.gs.Wipe.Paths); err != nil {
		return err
	}

	// 4. Bring it back up if it was running.
	if wasRunning {
		if err := s.recreateAndStart(ctx, id); err != nil {
			return fmt.Errorf("restart after wipe: %w", err)
		}
	}
	return nil
}

// wipePaths deletes glob matches of patterns under dataDir, refusing anything
// that resolves outside the data dir (traversal/symlink) or to the data dir root.
func (s *Server) wipePaths(dataDir string, patterns []string) (int, error) {
	base, err := filepath.Abs(dataDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" || strings.Contains(pat, "..") {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(base, pat))
		for _, m := range matches {
			rel, err := filepath.Rel(base, m)
			if err != nil {
				continue
			}
			safe, ok := safeJoin(base, rel)
			if !ok || safe == base { // never delete the data dir itself
				continue
			}
			if os.RemoveAll(safe) == nil {
				n++
			}
		}
	}
	return n, nil
}

func (s *Server) handleWipeServer(w http.ResponseWriter, r *http.Request) {
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
	if err := s.wipeServer(r.Context(), id, req.BackupFirst, req.TargetID); err != nil {
		jsonError(w, "wipe failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.auditLog(r, "server.wipe", "server:"+id, map[string]any{"backup_first": req.BackupFirst})
	s.notifyAll("🧹 " + srv.Name + " wiped")
	jsonOK(w, map[string]string{"status": "wiped"})
}
