package api

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/kristianwind/yggdrasil/internal/docker"
)

const (
	startupReadyDeadline = 10 * time.Minute // how long to wait for a readiness signal before falling back
	readinessScanTail    = "2000"           // log lines scanned per poll for the done_regex (games can be very chatty during load)
)

// watchStartupReady promotes a server from "starting" to "running" once it's
// actually up: when the gameskill's done_regex appears in the container logs (or,
// if there's no done_regex, once the container has stayed up briefly). If the
// container exits during startup it's set back to "stopped". Bounded so it can't
// run forever.
func (s *Server) watchStartupReady(serverID, containerID, doneRegex string) {
	s.watchStartupReadyFrom(serverID, containerID, doneRegex, time.Now())
}

// watchStartupReadyFrom is watchStartupReady with an explicit start time, so a
// watcher re-attached after a panel restart anchors its deadline on when the
// container actually started (not "now") — a container already past the deadline
// resolves immediately instead of waiting another full window.
func (s *Server) watchStartupReadyFrom(serverID, containerID, doneRegex string, start time.Time) {
	defer recoverLog("watchStartupReady")
	var re *regexp.Regexp
	if doneRegex != "" {
		re, _ = regexp.Compile(doneRegex)
	}
	deadline := start.Add(startupReadyDeadline)
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
		if rc, err := s.docker.LogsSnapshot(context.Background(), containerID, readinessScanTail); err == nil {
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

// resumeStartingWatchers re-attaches readiness detection to servers left in
// "starting" after a panel restart. The watchStartupReady goroutine lives only in
// memory, so a restart mid-start would otherwise strand a server in "starting"
// forever — its container up, but the panel never confirming (or failing) it.
// Runs once at boot, after Docker is confirmed reachable. For each stuck server:
//   - container gone → it exited while we were down: mark stopped
//   - no done_regex expected → container up is the only signal: mark running
//   - done_regex already somewhere in the full log → it became ready: mark running
//   - done_regex expected, not yet seen, container still young → re-attach a watcher
//   - done_regex expected, never seen, container older than the ready window → it
//     came up but never became ready (e.g. a game process that failed and left its
//     wrapper alive): mark stopped and alert
func (s *Server) resumeStartingWatchers(ctx context.Context) {
	defer recoverLog("resumeStartingWatchers")
	rows, err := s.db.Query("SELECT id, COALESCE(container_id,'') FROM servers WHERE status='starting' AND container_id<>''")
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
		running, _, err := s.docker.State(ctx, x.cid)
		if err != nil || !running {
			s.db.Exec("UPDATE servers SET status='stopped' WHERE id=? AND status='starting'", x.id)
			continue
		}
		doneRegex := ""
		if rt, err := s.loadRuntime(ctx, x.id); err == nil {
			doneRegex = rt.gs.Startup.DoneRegex
		}
		if doneRegex == "" {
			s.markStarted(x.id) // container up + no readiness signal expected = running
			continue
		}
		if s.containerLogMatches(ctx, x.cid, doneRegex) {
			log.Printf("resume: %s reached readiness before the restart — marking running", x.id)
			s.markStarted(x.id)
			continue
		}
		started, _ := s.docker.StartedAt(ctx, x.cid)
		if !started.IsZero() && time.Since(started) > startupReadyDeadline {
			// Up well past the ready window yet never signaled — a failed start whose
			// container is still alive (e.g. the game process died but its wrapper didn't).
			log.Printf("resume: %s up %s without readiness — marking stopped", x.id, time.Since(started).Round(time.Second))
			s.db.Exec("UPDATE servers SET status='stopped' WHERE id=? AND status='starting'", x.id)
			s.notifyStartStalled(x.id, x.cid)
			continue
		}
		// Still plausibly starting — re-attach a live watcher for the rest of the window.
		go s.watchStartupReadyFrom(x.id, x.cid, doneRegex, started)
	}
}

// containerLogMatches reports whether doneRegex appears anywhere in a container's
// full log — used on resume to catch a readiness signal that already scrolled far
// past the live watcher's tail window.
func (s *Server) containerLogMatches(ctx context.Context, containerID, doneRegex string) bool {
	re, err := regexp.Compile(doneRegex)
	if err != nil {
		return false
	}
	rc, err := s.docker.LogsSnapshot(ctx, containerID, "all")
	if err != nil {
		return false
	}
	defer rc.Close()
	var buf bytes.Buffer
	_ = docker.DemuxCopy(&buf, rc)
	return re.Match(buf.Bytes())
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

	// Docker is confirmed up now — re-attach readiness detection to any server left
	// mid-"starting" by this restart (its watcher goroutine didn't survive).
	s.resumeStartingWatchers(ctx)
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
		running, exitCode, err := s.docker.State(context.Background(), x.cid)
		if err != nil || !running {
			// Container exited (crash or external stop) — mark stopped and release
			// any port-forward rules so they don't linger pointing at a dead port.
			// Drop any watchdog streak so it can't heal a server the user stopped.
			// When the container is still present (err==nil) we know WHY it exited, so
			// log it to the stability history first — this is the once-per-transition
			// signal that a silently-dying server used to leave no trace of.
			if err == nil {
				s.recordCrash(x.id, x.cid, exitCode)
			}
			s.db.Exec("UPDATE servers SET status='stopped' WHERE id=?", x.id)
			s.clearWatchdog(x.id)
			s.clearResourceAlarms(x.id)
			s.stoppedCleanup(x.id)
		}
	}
}

// recordCrash logs an unexpected container exit to the stability history, grabbing
// the tail of the container log as the likely reason. A non-zero exit code is a
// real crash and gets a notification (the gap that let today's fleet die in
// silence); exit 0 is a clean external stop — logged quietly, no alert.
func (s *Server) recordCrash(serverID, containerID string, exitCode int) {
	defer recoverLog("recordCrash")
	reason := s.lastLogLines(containerID, "15")
	s.db.Exec("INSERT INTO server_crashes (server_id, exit_code, reason) VALUES (?,?,?)", serverID, exitCode, reason)
	// Only a genuine fault is worth an alert. 0, 143 (SIGTERM) and 130 (SIGINT) are
	// graceful terminations — a docker stop, a restart, or a host reboot — not crashes.
	if isCrashExit(exitCode) {
		name := s.serverName(serverID)
		msg := fmt.Sprintf("💥 %s exited unexpectedly (code %d)", name, exitCode)
		if reason != "" {
			msg += "\n```\n" + reason + "\n```"
		}
		go s.notifyServer(serverID, msg)
		go s.kvasirReact(serverID, "crash", fmt.Sprintf("exit %d", exitCode), reason)
	}
}

// isCrashExit reports whether a container exit code is a real fault rather than a
// graceful stop signal (0 = clean, 143 = SIGTERM, 130 = SIGINT).
func isCrashExit(code int) bool {
	return code != 0 && code != 143 && code != 130
}

// lastLogLines returns the tail of a container's log, trimmed and length-capped,
// for the crash-reason snippet. Best-effort: empty string on any error.
func (s *Server) lastLogLines(containerID, tail string) string {
	rc, err := s.docker.LogsSnapshot(context.Background(), containerID, tail)
	if err != nil {
		return ""
	}
	defer rc.Close()
	var buf bytes.Buffer
	_ = docker.DemuxCopy(&buf, rc)
	out := strings.TrimSpace(buf.String())
	const max = 800
	if len(out) > max {
		out = "…" + out[len(out)-max:]
	}
	return out
}

// stoppedCleanup releases UPnP/UniFi port forwards and the NPM proxy host for a
// server that has stopped/crashed (best-effort, async).
func (s *Server) stoppedCleanup(serverID string) {
	go s.upnpRemoveServer(serverID)
	go s.unifiRemoveServer(serverID)
	go s.npmRemoveServer(serverID)
	go s.cfRemoveServer(serverID)
}
