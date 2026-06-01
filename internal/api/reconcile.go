package api

import (
	"bytes"
	"context"
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
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		running, _, err := s.docker.State(context.Background(), containerID)
		if err != nil || !running {
			s.db.Exec("UPDATE servers SET status='stopped' WHERE id=? AND status='starting'", serverID)
			return
		}
		if re == nil {
			// No readiness signal — the container is up, call it running.
			s.db.Exec("UPDATE servers SET status='running' WHERE id=? AND status='starting'", serverID)
			return
		}
		if rc, err := s.docker.LogsSnapshot(context.Background(), containerID, "500"); err == nil {
			var buf bytes.Buffer
			_ = docker.DemuxCopy(&buf, rc)
			rc.Close()
			if re.Match(buf.Bytes()) {
				s.db.Exec("UPDATE servers SET status='running' WHERE id=? AND status='starting'", serverID)
				return
			}
		}
	}
	// Took too long to signal readiness but it's still up — show it as running.
	if running, _, _ := s.docker.State(context.Background(), containerID); running {
		s.db.Exec("UPDATE servers SET status='running' WHERE id=? AND status='starting'", serverID)
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
			s.db.Exec("UPDATE servers SET status='stopped' WHERE id=?", x.id)
			s.stoppedCleanup(x.id)
		}
	}
}

// stoppedCleanup releases UPnP/UniFi port forwards for a server that has
// stopped/crashed (best-effort, async).
func (s *Server) stoppedCleanup(serverID string) {
	go s.upnpRemoveServer(serverID)
	go s.unifiRemoveServer(serverID)
}
