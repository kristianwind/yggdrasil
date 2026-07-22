package api

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/kristianwind/yggdrasil/internal/docker"
)

// Start-failure detection ("start watchdog"). The regular watchdog only heals a
// server the panel already believes is *running* — a container that comes up but
// its game process crashes straight back out during startup is caught by nothing,
// and watchStartupReady would silently flip it back to "stopped" with no signal.
// (Real case: a Bedrock server that can't reach the online-mode services exits a
// few seconds after start.)
//
// So when a start attempt fails, we retry a bounded number of times — a transient
// blip (slow image pull, a dependency still coming up) self-heals without bothering
// anyone — and if it still won't stay up after startMaxAttempts, we leave it stopped
// and send ONE actionable alert with the container's last log lines, so the admin
// knows why. Retries are logged, not notified, to keep the happy-ish path quiet.
//
// State lives in memory (like the watchdog): a per-server consecutive-failure count
// that resets the moment a start succeeds, or the operator manually starts/stops the
// server. Bounded + reset-on-success means it can never loop or flap forever.
const (
	startMaxAttempts     = 3                // total start attempts (1 original + startMaxAttempts-1 auto-retries) before giving up
	startFailRetryDelay  = 15 * time.Second // backoff before an auto-retry, so a dependency has a moment to come up
	startupLogTailLines  = "40"             // how many container log lines to attach to the give-up alert
	startupLogTailMaxLen = 1500             // …trimmed to this many characters so notifications stay small
	slowStartWarnAfter   = 5 * time.Minute  // still "starting" this long (no crash) → one "taking a long time" heads-up
)

type startState struct {
	mu       sync.Mutex
	attempts map[string]int // serverID -> consecutive failed start attempts
}

func newStartState() *startState {
	return &startState{attempts: map[string]int{}}
}

// recordFailure bumps and returns a server's consecutive failed-start count.
func (st *startState) recordFailure(serverID string) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.attempts[serverID]++
	return st.attempts[serverID]
}

// reset clears the failure streak — called on a successful start, and whenever the
// operator manually starts/stops/deletes the server (a fresh intent gets a fresh
// retry budget).
func (st *startState) reset(serverID string) {
	st.mu.Lock()
	delete(st.attempts, serverID)
	st.mu.Unlock()
}

func (s *Server) clearStartWatch(serverID string) {
	if s.startWD != nil {
		s.startWD.reset(serverID)
	}
}

// onStartFailed is invoked by watchStartupReady when a server that was "starting"
// dropped back to "stopped" (its container exited before it ever became ready). It
// decides whether to auto-retry or give up, and alerts on give-up. Runs inside the
// watchStartupReady goroutine, so the retry chain is strictly linear — one live
// watcher per server at a time.
func (s *Server) onStartFailed(serverID, containerID string) {
	defer recoverLog("onStartFailed")
	if s.startWD == nil {
		return
	}
	// Grab the log tail now, while the crashed container still exists (a retry
	// recreates it and this evidence is gone).
	tail := s.startupLogTail(containerID)
	name := s.serverName(serverID)
	n := s.startWD.recordFailure(serverID)

	if n < startMaxAttempts {
		log.Printf("start-failure: %s (%s) failed to start (attempt %d/%d); retrying in %s",
			name, serverID, n, startMaxAttempts, startFailRetryDelay)
		time.Sleep(startFailRetryDelay)
		// Bail out if the operator (or an install) took over during the backoff — the
		// server should only still be "stopped" if nobody has touched it since we did.
		var status string
		if err := s.db.QueryRow("SELECT status FROM servers WHERE id=?", serverID).Scan(&status); err != nil {
			s.startWD.reset(serverID) // row gone (deleted) — drop the streak
			return
		}
		if status != "stopped" || s.install.isActive(serverID) {
			return // someone else is driving this server now; leave its streak intact
		}
		if err := s.recreateAndStart(context.Background(), serverID); err != nil {
			// Couldn't even launch the retry — that's terminal, so alert now instead
			// of waiting for a watcher that will never be spawned.
			s.startWD.reset(serverID)
			s.notifyStartGaveUp(name, tail, fmt.Sprintf("restart failed: %v", err))
		}
		// Otherwise a fresh watchStartupReady is now watching the retry.
		return
	}

	// Exhausted the budget — leave it stopped and raise the alarm once.
	s.startWD.reset(serverID)
	s.notifyStartGaveUp(name, tail, "")
}

// notifySlowStart raises a one-time heads-up that a server is taking unusually long
// to become ready (it hasn't crashed — the start-watchdog's failure path wouldn't
// fire). The attached log tail lets the AI (and the operator) see what it's stuck
// on. Called at most once per start attempt.
func (s *Server) notifySlowStart(serverID, containerID string) {
	defer recoverLog("notifySlowStart")
	name := s.serverName(serverID)
	mins := int(slowStartWarnAfter.Minutes())
	msg := fmt.Sprintf("⏳ %s is taking longer than usual to start — still not ready after %d min. "+
		"It's still trying; check the console for what it's doing.", name, mins)
	if tail := s.startupLogTail(containerID); tail != "" {
		msg += "\nLatest log:\n" + tail
	}
	s.notifyServer(serverID, msg)
	go s.kvasirReact(serverID, "slowstart", "taking longer than usual to become ready", s.startupLogTail(containerID))
}

// notifyStartStalled alerts that a server which was still "starting" when the panel
// restarted has been found up-but-never-ready and marked stopped (its game process
// likely failed while the container's wrapper stayed alive). Attaches the log tail.
func (s *Server) notifyStartStalled(serverID, containerID string) {
	defer recoverLog("notifyStartStalled")
	name := s.serverName(serverID)
	msg := fmt.Sprintf("🛑 %s was still starting and never became ready — its container is up but the game isn't. "+
		"Marked stopped so you can restart it.", name)
	if tail := s.startupLogTail(containerID); tail != "" {
		msg += "\nLast log:\n" + tail
	}
	s.notifyServer(serverID, msg)
	go s.kvasirReact(serverID, "slowstart", "taking longer than usual to become ready", s.startupLogTail(containerID))
}

// notifyStartGaveUp sends the single actionable "couldn't start" alert, attaching
// the container's last log lines (and an optional extra reason) so the admin can act.
func (s *Server) notifyStartGaveUp(name, logTail, extra string) {
	msg := fmt.Sprintf("🛑 %s failed to start after %d attempts and has been left stopped.", name, startMaxAttempts)
	if extra != "" {
		msg += " " + extra
	}
	if logTail != "" {
		msg += "\nLast log:\n" + logTail
	}
	s.notifyAll(msg)
}

// startupLogTail returns the tail of a container's logs, trimmed for a notification.
func (s *Server) startupLogTail(containerID string) string {
	if containerID == "" {
		return ""
	}
	rc, err := s.docker.LogsSnapshot(context.Background(), containerID, startupLogTailLines)
	if err != nil {
		return ""
	}
	defer rc.Close()
	var buf bytes.Buffer
	_ = docker.DemuxCopy(&buf, rc)
	out := strings.TrimSpace(buf.String())
	if len(out) > startupLogTailMaxLen {
		out = "…" + out[len(out)-startupLogTailMaxLen:]
	}
	return out
}
