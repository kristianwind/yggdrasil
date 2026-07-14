package api

import (
	"fmt"
	"sync"
)

// Per-server resource alarms. When a running server's CPU% or memory stays at or
// above a per-server threshold across alarmBreaches consecutive samples (the
// sampler runs every metricsInterval, so this is "sustained", not a one-off
// spike), the panel fires ONE notification and won't repeat it until the metric
// drops back below — then it sends an all-clear. Edge-triggered, so no alert spam.
// Off unless a threshold is set on the server.

const alarmBreaches = 2 // consecutive over-threshold samples before alarming (~2×metricsInterval)

type alarmState struct {
	mu        sync.Mutex
	cpuOver   map[string]int  // serverID -> consecutive CPU breaches
	memOver   map[string]int  // serverID -> consecutive memory breaches
	cpuFiring map[string]bool // serverID -> CPU alarm currently active
	memFiring map[string]bool
}

func newAlarmState() *alarmState {
	return &alarmState{
		cpuOver: map[string]int{}, memOver: map[string]int{},
		cpuFiring: map[string]bool{}, memFiring: map[string]bool{},
	}
}

// alarmStep folds one sample against a threshold and returns the edge event:
// fire=true when it crosses INTO alarming, clear=true when it recovers. A disabled
// threshold (<=0) resets state and never fires. Caller holds the lock.
func alarmStep(over map[string]int, firing map[string]bool, id string, value, threshold float64) (fire, clear bool) {
	if threshold <= 0 {
		over[id] = 0
		if firing[id] {
			firing[id] = false
		}
		return false, false
	}
	if value >= threshold {
		over[id]++
		if over[id] >= alarmBreaches && !firing[id] {
			firing[id] = true
			return true, false
		}
		return false, false
	}
	over[id] = 0
	if firing[id] {
		firing[id] = false
		return false, true
	}
	return false, false
}

// checkResourceAlarms evaluates one server's latest CPU/mem sample and notifies on
// any alarm/recovery edge. Called from the metrics sampler for each running server.
func (s *Server) checkResourceAlarms(serverID string, cpu, memMB float64) {
	if s.alarms == nil {
		return
	}
	var cpuTh, memTh int
	if s.db.QueryRow("SELECT COALESCE(cpu_alarm_pct,0), COALESCE(mem_alarm_mb,0) FROM servers WHERE id=?", serverID).
		Scan(&cpuTh, &memTh) != nil {
		return
	}
	s.alarms.mu.Lock()
	cpuFire, cpuClear := alarmStep(s.alarms.cpuOver, s.alarms.cpuFiring, serverID, cpu, float64(cpuTh))
	memFire, memClear := alarmStep(s.alarms.memOver, s.alarms.memFiring, serverID, memMB, float64(memTh))
	s.alarms.mu.Unlock()

	if !cpuFire && !cpuClear && !memFire && !memClear {
		return
	}
	name := s.serverName(serverID)
	mins := alarmBreaches * int(metricsInterval.Minutes())
	if cpuFire {
		s.notifyAll(fmt.Sprintf("⚠️ %s CPU high: %.0f%% (≥ %d%% for ~%d min)", name, cpu, cpuTh, mins))
	}
	if cpuClear {
		s.notifyAll(fmt.Sprintf("✅ %s CPU back to normal (%.0f%%)", name, cpu))
	}
	if memFire {
		s.notifyAll(fmt.Sprintf("⚠️ %s memory high: %.0f MB (≥ %d MB for ~%d min)", name, memMB, memTh, mins))
	}
	if memClear {
		s.notifyAll(fmt.Sprintf("✅ %s memory back to normal (%.0f MB)", name, memMB))
	}
}

// clearResourceAlarms drops a server's alarm state — called when it stops, so a
// fresh run re-evaluates from scratch instead of staying latched.
func (s *Server) clearResourceAlarms(serverID string) {
	if s.alarms == nil {
		return
	}
	s.alarms.mu.Lock()
	delete(s.alarms.cpuOver, serverID)
	delete(s.alarms.memOver, serverID)
	delete(s.alarms.cpuFiring, serverID)
	delete(s.alarms.memFiring, serverID)
	s.alarms.mu.Unlock()
}
