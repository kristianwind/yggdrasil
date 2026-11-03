package api

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// A server can end with a clean exit while nobody asked it to stop, and
// Docker will not bring it back: the restart policy the panel sets is
// on-failure, and exit 0 is not a failure. cloudflared is the case that kept
// happening. It exits 0 on SIGTERM, so a tunnel that something outside the
// panel stopped stayed down. The reconciler then marked it stopped and, through
// stoppedCleanup, deleted its tunnel routes and DNS records. autostart also
// skips a server marked stopped, so not even a reboot brought it back.
//
// For a server with autostart on, that clean exit is now treated as the server
// going away rather than being turned off: the reconciler starts it again and
// leaves its routes alone. Three things keep that from fighting the operator:
//   - a stop the panel asked for is remembered (docker.Client.StoppedByPanel),
//     including one begun by typing the rune's stop command;
//   - a crash exit is left to Docker's own restart policy, as before;
//   - at most reviveBudget revivals per reviveWindow. A server that keeps
//     exiting is stopped as it was before, so a broken one doesn't restart
//     forever.

const (
	reviveBudget = 3
	reviveWindow = time.Hour
	// How long after a panel stop the exit counts as that stop. It covers the
	// longest graceful stop a rune can ask for, with margin.
	panelStopMemory = 15 * time.Minute
)

type reviveState struct {
	mu   sync.Mutex
	hist map[string][]time.Time
}

// take spends one revival for a server, reporting false when the window's
// budget is already used.
func (r *reviveState) take(serverID string, now time.Time) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hist == nil {
		r.hist = map[string][]time.Time{}
	}
	kept := r.hist[serverID][:0]
	for _, t := range r.hist[serverID] {
		if now.Sub(t) < reviveWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= reviveBudget {
		r.hist[serverID] = kept
		return len(kept), false
	}
	r.hist[serverID] = append(kept, now)
	return len(kept) + 1, true
}

// matchesStopCommand reports whether a console command is the rune's own stop
// command ("stop" and "/stop" are the same command to a Minecraft server).
func matchesStopCommand(gs *gameskill.Gameskill, command string) bool {
	if gs == nil || gs.Startup.Stop == "" {
		return false
	}
	norm := func(c string) string {
		return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(c), "/"))
	}
	return norm(command) == norm(gs.Startup.Stop)
}

func (s *Server) isStopCommand(ctx context.Context, serverID, command string) bool {
	rt, err := s.loadRuntime(ctx, serverID)
	return err == nil && matchesStopCommand(rt.gs, command)
}

// reviveCandidate reports whether a server whose container exited should be
// started again rather than marked stopped. It doesn't touch Docker.
func (s *Server) reviveCandidate(serverID, containerID string, exitCode int) bool {
	if isCrashExit(exitCode) || !s.shouldRecordCrash(serverID) {
		return false
	}
	if s.docker != nil && s.docker.StoppedByPanel(containerID, panelStopMemory) {
		return false
	}
	var autostart int
	s.db.QueryRow("SELECT autostart FROM servers WHERE id=?", serverID).Scan(&autostart) //nolint:errcheck
	return autostart == 1
}

// reviveServer starts an exited container in place, its sidecars first, and
// reports whether it is running again.
func (s *Server) reviveServer(serverID, containerID string, exitCode int) bool {
	name := s.serverName(serverID)
	n, ok := s.revive.take(serverID, time.Now())
	if !ok {
		log.Printf("reconcile: %s (%s) exited with code %d again — started %d times in the last hour already, leaving it stopped",
			name, serverID, exitCode, n)
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s.startStackForAutostart(ctx, serverID)
	if err := s.docker.Start(ctx, containerID); err != nil {
		log.Printf("reconcile: %s (%s) exited with code %d without the panel stopping it, and could not be started again: %v",
			name, serverID, exitCode, err)
		return false
	}
	if running, _, err := s.docker.State(ctx, containerID); err != nil || !running {
		log.Printf("reconcile: %s (%s) exited with code %d and did not stay up when started again", name, serverID, exitCode)
		return false
	}
	log.Printf("reconcile: %s (%s) exited with code %d without the panel stopping it — started it again (%d/%d this hour)",
		name, serverID, exitCode, n, reviveBudget)
	return true
}
