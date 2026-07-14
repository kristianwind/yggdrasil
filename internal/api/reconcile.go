package api

import (
	"bytes"
	"context"
	"log"
	"regexp"
	"time"

	"github.com/kristianwind/yggdrasil/internal/docker"
)

// watchStartupReady promotes a server from "starting" to "running" once it's
// actually up: when the gameskill's done_regex appears in the container logs (or,
// if there's no done_regex, once the container has stayed up briefly). If the
// container exits during startup it's set back to "stopped". Bounded so it can't
// run forever.
func (s *Server) watchStartupReady(serverID, containerID, doneRegex string) {
	defer recoverLog("watchStartupReady")
	var re *regexp.Regexp
	if doneRegex != "" {
		re, _ = regexp.Compile(doneRegex)
	}
	start := time.Now()
	deadline := start.Add(10 * time.Minute)
	warnedSlow := false
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		// A server that stays in "starting" far longer than usual (but hasn't crashed)
		// is a soft failure the start-watchdog's crash path misses — surface it once so
		// it's visible (and AI-explainable from the container logs), then keep waiting.
		if !warnedSlow && re != nil && time.Since(start) >= slowStartWarnAfter {
			warnedSlow = true
			go s.notifySlowStart(serverID, containerID)
		}
		running, _, err := s.docker.State(context.Background(), containerID)
		if err != nil || !running {
			// The container exited before it ever became ready. Only treat this as a
			// start-failure if WE are the ones flipping starting→stopped (RowsAffected>0):
			// a 0-row update means a newer start/stop already superseded this attempt.
			res, _ := s.db.Exec("UPDATE servers SET status='stopped' WHERE id=? AND status='starting'", serverID)
			if n, _ := res.RowsAffected(); n > 0 {
				s.onStartFailed(serverID, containerID)
			}
			return
		}
		if re == nil {
			// No readiness signal — the container is up, call it running.
			s.markStarted(serverID)
			return
		}
		if rc, err := s.docker.LogsSnapshot(context.Background(), containerID, "500"); err == nil {
			var buf bytes.Buffer
			_ = docker.DemuxCopy(&buf, rc)
			rc.Close()
			if re.Match(buf.Bytes()) {
				s.markStarted(serverID)
				return
			}
		}
	}
	// Took too long to signal readiness but it's still up — show it as running.
	if running, _, _ := s.docker.State(context.Background(), containerID); running {
		s.markStarted(serverID)
	}
}

// markStarted promotes a server that reached readiness to "running" and clears its
// failed-start streak (a clean start earns back the full retry budget).
func (s *Server) markStarted(serverID string) {
	s.db.Exec("UPDATE servers SET status='running' WHERE id=? AND status='starting'", serverID)
	s.clearStartWatch(serverID)
}

// startAutostartServers brings autostart-enabled servers back up after a panel
// or host restart. It restarts only servers that were RUNNING (per the DB, which
// persists across a reboot) but whose container is now down — so:
//   - a real host reboot (containers stopped) restarts them,
//   - a plain panel restart / deploy (game containers keep running) is a no-op,
//   - a server the user manually stopped (status 'stopped') stays stopped.
//
// Runs once at startup; bounded waiting for the Docker daemon on a cold boot.
func (s *Server) startAutostartServers() {
	defer recoverLog("startAutostartServers")
	ctx := context.Background()
	// On a cold boot the panel may come up just before dockerd; wait for it.
	for i := 0; i < 10; i++ {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := s.docker.Ping(c)
		cancel()
		if err == nil {
			break
		}
		time.Sleep(3 * time.Second)
	}

	rows, err := s.db.Query(
		"SELECT id, COALESCE(container_id,'') FROM servers WHERE autostart=1 AND installed=1 AND status IN ('running','starting')")
	if err != nil {
		return
	}
	type sv struct{ id, cid string }
	var list []sv
	for rows.Next() {
		var x sv
		if rows.Scan(&x.id, &x.cid) == nil {
			list = append(list, x)
		}
	}
	rows.Close()

	for _, x := range list {
		if x.cid != "" {
			if running, _, err := s.docker.State(ctx, x.cid); err == nil && running {
				continue // still up (panel restarted but the container kept running)
			}
		}
		if err := s.recreateAndStart(ctx, x.id); err != nil {
			log.Printf("autostart: server %s failed to start: %v", x.id, err)
		}
	}
}

// startStatusReconciler periodically checks servers marked "running" and flips
// them to "stopped" if their container is no longer running — so the UI reflects
// crashes/exits and the console doesn't try to attach to a dead container.
func (s *Server) startStatusReconciler() {
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for range t.C {
			s.reconcileStatuses()
			s.runWatchdog()
		}
	}()
}

func (s *Server) reconcileStatuses() {
	rows, err := s.db.Query("SELECT id, COALESCE(container_id,'') FROM servers WHERE status IN ('running','starting') AND container_id<>''")
	if err != nil {
		return
	}
	type sv struct{ id, cid string }
	var list []sv
	for rows.Next() {
		var x sv
		if rows.Scan(&x.id, &x.cid) == nil {
			list = append(list, x)
		}
	}
	rows.Close()

	for _, x := range list {
		running, _, err := s.docker.State(context.Background(), x.cid)
		if err != nil || !running {
			// Container exited (crash or external stop) — mark stopped and release
			// any port-forward rules so they don't linger pointing at a dead port.
			// Drop any watchdog streak so it can't heal a server the user stopped.
			s.db.Exec("UPDATE servers SET status='stopped' WHERE id=?", x.id)
			s.clearWatchdog(x.id)
			s.stoppedCleanup(x.id)
		}
	}
}

// stoppedCleanup releases UPnP/UniFi port forwards and the NPM proxy host for a
// server that has stopped/crashed (best-effort, async).
func (s *Server) stoppedCleanup(serverID string) {
	go s.upnpRemoveServer(serverID)
	go s.unifiRemoveServer(serverID)
	go s.npmRemoveServer(serverID)
	go s.cfRemoveServer(serverID)
}
