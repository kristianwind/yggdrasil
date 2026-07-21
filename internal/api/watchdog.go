package api

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/query"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// Watchdog (auto-heal): for servers with the toggle on, the reconciler tick
// health-checks the game via its query protocol. When a server the panel thinks
// is running fails watchdogFailThreshold checks in a row — the container is up
// but the game is hung/deadlocked (a class of failure a plain container-liveness
// check misses) — the watchdog auto-restarts it and notifies. A cooldown after a
// heal prevents a restart loop while the server comes back up.
//
// It's the deterministic "hands" for keeping a server alive; the AI layer can
// later flip the same toggle or tune the threshold. Only runes that declare a
// query can be health-checked, so the UI gates the toggle on watchdog_supported.
const (
	watchdogFailThreshold = 3                // consecutive failed health checks before an auto-restart
	watchdogCooldown      = 4 * time.Minute  // grace period after a heal before checking again (let it boot)
	quarantineThreshold   = 5                // heals within the window before we give up (crash-loop guard)
	quarantineWindow      = 30 * time.Minute // window over which heals are counted
)

type watchdogState struct {
	mu         sync.Mutex
	fails      map[string]int         // serverID -> consecutive failed health checks
	cooldown   map[string]time.Time   // serverID -> skip checks until this time
	heals      map[string][]time.Time // serverID -> recent heal times (crash-loop detection)
	quarantine map[string]bool        // serverID -> auto-heal paused (kept failing)
}

func newWatchdogState() *watchdogState {
	return &watchdogState{
		fails: map[string]int{}, cooldown: map[string]time.Time{},
		heals: map[string][]time.Time{}, quarantine: map[string]bool{},
	}
}

// recordHeal logs a heal and reports whether this one tips the server into
// quarantine — too many restarts in the window means auto-heal isn't fixing it, so
// we stop trying and alert instead of flapping forever. Returns true only on the
// transition into quarantine (so the caller alerts once).
func (w *watchdogState) recordHeal(serverID string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	cutoff := now.Add(-quarantineWindow)
	var kept []time.Time
	for _, t := range w.heals[serverID] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	w.heals[serverID] = kept
	if len(kept) >= quarantineThreshold && !w.quarantine[serverID] {
		w.quarantine[serverID] = true
		return true
	}
	return false
}

func (w *watchdogState) isQuarantined(serverID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.quarantine[serverID]
}

// inCooldown reports whether a server is still in its post-heal grace period (so
// the caller skips health-checking it). Expired cooldowns are cleared.
func (w *watchdogState) inCooldown(serverID string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	until, ok := w.cooldown[serverID]
	if !ok {
		return false
	}
	if now.Before(until) {
		return true
	}
	delete(w.cooldown, serverID)
	return false
}

// recordResult folds one health-check outcome into a server's failure streak and
// reports whether a heal should fire now. A success resets the streak; the
// threshold'th consecutive failure returns heal=true, resets the streak, and arms
// the cooldown so the reboot has room to complete before the next check.
func (w *watchdogState) recordResult(serverID string, ok bool, now time.Time) (heal bool, fails int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ok {
		delete(w.fails, serverID)
		return false, 0
	}
	w.fails[serverID]++
	n := w.fails[serverID]
	if n < watchdogFailThreshold {
		return false, n
	}
	delete(w.fails, serverID)
	w.cooldown[serverID] = now.Add(watchdogCooldown)
	return true, n
}

// runWatchdog runs one health-check pass over all watchdog-enabled running
// servers. Called from the status reconciler's 20s tick.
func (s *Server) runWatchdog() {
	defer recoverLog("runWatchdog")
	if s.wd == nil {
		return
	}
	rows, err := s.db.Query(
		"SELECT id, COALESCE(container_id,'') FROM servers WHERE watchdog=1 AND status='running' AND container_id<>''")
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
		now := time.Now()
		// A crash-looping server is left alone until an admin starts it manually.
		if s.wd.isQuarantined(x.id) || s.wd.inCooldown(x.id, now) {
			continue
		}
		// Only servers whose rune declares a query can be health-checked.
		rt, err := s.loadRuntime(context.Background(), x.id)
		if err != nil || rt.gs.Query == nil {
			continue
		}
		_, qerr := query.Query(rt.gs.Query.Type, "127.0.0.1", rt.queryPort(), 3*time.Second)
		if heal, n := s.wd.recordResult(x.id, qerr == nil, now); heal {
			if s.wd.recordHeal(x.id, now) {
				go s.watchdogQuarantine(x.id)
			} else {
				go s.watchdogHeal(x.id, n)
			}
		}
	}
}

// watchdogHeal recreates a hung server and notifies. Runs in its own goroutine
// since recreateAndStart blocks on an image (re)pull + container start.
func (s *Server) watchdogHeal(serverID string, fails int) {
	defer recoverLog("watchdogHeal")
	name := s.serverName(serverID)
	s.notifyServer(serverID, fmt.Sprintf("🩺 Watchdog: %s stopped responding (%d failed checks) — auto-restarting", name, fails))
	if err := s.recreateAndStart(context.Background(), serverID); err != nil {
		s.notifyServer(serverID, fmt.Sprintf("❌ Watchdog: restarting %s failed: %v", name, err))
		return
	}
	s.notifyServer(serverID, fmt.Sprintf("✅ Watchdog: %s restarted", name))
}

// watchdogQuarantine alerts that auto-heal has given up on a crash-looping server.
func (s *Server) watchdogQuarantine(serverID string) {
	defer recoverLog("watchdogQuarantine")
	name := s.serverName(serverID)
	s.notifyServer(serverID, fmt.Sprintf("🛑 Watchdog: giving up on %s — it kept failing after %d auto-restarts within %d min. "+
		"Auto-heal is paused; fix the server and start it manually to resume.",
		name, quarantineThreshold, int(quarantineWindow.Minutes())))
}

// clearWatchdog drops all in-memory watchdog state for a server (streak, cooldown,
// heal history, quarantine) — called when the toggle is turned off, the server is
// stopped/deleted, or it's manually (re)started, so a fresh chance is given.
func (s *Server) clearWatchdog(serverID string) {
	if s.wd == nil {
		return
	}
	s.wd.mu.Lock()
	delete(s.wd.fails, serverID)
	delete(s.wd.cooldown, serverID)
	delete(s.wd.heals, serverID)
	delete(s.wd.quarantine, serverID)
	s.wd.mu.Unlock()
}

// handleSetWatchdog toggles auto-heal for a server.
func (s *Server) handleSetWatchdog(w http.ResponseWriter, r *http.Request) {
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
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	s.db.ExecContext(r.Context(), "UPDATE servers SET watchdog=? WHERE id=?", boolToInt(req.Enabled), id)
	if !req.Enabled {
		s.clearWatchdog(id)
	}
	s.auditLog(r, "server.watchdog", "server:"+id, map[string]any{"enabled": req.Enabled})
	jsonOK(w, map[string]bool{"watchdog": req.Enabled})
}
