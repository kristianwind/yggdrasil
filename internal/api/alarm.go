package api

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Per-server resource alarms. When a running server's CPU% or memory stays at or
// above a per-server threshold across alarmBreaches consecutive samples (the
// sampler runs every metricsInterval, so this is "sustained", not a one-off
// spike), the panel fires ONE notification and won't repeat it until the metric
// drops back below — then it sends an all-clear. Edge-triggered, so no alert spam.
// Off unless a threshold is set on the server.

const alarmBreaches = 2 // consecutive over-threshold samples before alarming (~2×metricsInterval)

type alarmState struct {
	mu         sync.Mutex
	cpuOver    map[string]int  // serverID -> consecutive CPU breaches
	memOver    map[string]int  // serverID -> consecutive memory breaches
	cpuFiring  map[string]bool // serverID -> CPU alarm currently active
	memFiring  map[string]bool
	diskFiring map[string]bool // serverID -> data-dir disk alarm currently active
}

func newAlarmState() *alarmState {
	return &alarmState{
		cpuOver: map[string]int{}, memOver: map[string]int{},
		cpuFiring: map[string]bool{}, memFiring: map[string]bool{},
		diskFiring: map[string]bool{},
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

// clearResourceAlarms drops a server's CPU/memory alarm state — called when it
// stops, so a fresh run re-evaluates from scratch instead of staying latched. Disk
// state is left alone: a stopped server's data dir keeps taking space, so its disk
// alarm should persist across restarts.
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

// Disk alarms watch each server's data-directory size (which, unlike CPU/mem,
// grows even while stopped — worlds, backups-in-place, logs). Checked on a slow
// timer since measuring means walking the tree. Edge-triggered like the others.

const diskAlarmInterval = 1 * time.Hour

func (s *Server) startDiskAlarmLoop() {
	go func() {
		defer recoverLog("diskAlarmLoop")
		time.Sleep(2 * time.Minute) // let the panel settle before the first (I/O-heavy) sweep
		s.checkDiskAlarms()
		t := time.NewTicker(diskAlarmInterval)
		defer t.Stop()
		for range t.C {
			s.checkDiskAlarms()
		}
	}()
}

// checkDiskAlarms measures the data dir of every server with a disk threshold set
// and fires/clears its alarm.
func (s *Server) checkDiskAlarms() {
	defer recoverLog("checkDiskAlarms")
	rows, err := s.db.Query("SELECT id, data_dir, disk_alarm_mb FROM servers WHERE COALESCE(disk_alarm_mb,0) > 0 AND data_dir <> ''")
	if err != nil {
		return
	}
	type sv struct {
		id, dir string
		thMB    int64
	}
	var list []sv
	for rows.Next() {
		var x sv
		if rows.Scan(&x.id, &x.dir, &x.thMB) == nil {
			list = append(list, x)
		}
	}
	rows.Close()

	for _, x := range list {
		s.evalDiskAlarm(x.id, dirSizeMB(x.dir), x.thMB)
	}
}

// evalDiskAlarm applies one measurement to the edge-triggered disk alarm.
func (s *Server) evalDiskAlarm(serverID string, sizeMB, thresholdMB int64) {
	if s.alarms == nil || thresholdMB <= 0 {
		return
	}
	s.alarms.mu.Lock()
	firing := s.alarms.diskFiring[serverID]
	var fire, clear bool
	switch {
	case sizeMB >= thresholdMB && !firing:
		s.alarms.diskFiring[serverID] = true
		fire = true
	case sizeMB < thresholdMB && firing:
		s.alarms.diskFiring[serverID] = false
		clear = true
	}
	s.alarms.mu.Unlock()

	if fire {
		s.notifyAll(fmt.Sprintf("💾 %s disk usage high: %d MB (≥ %d MB)", s.serverName(serverID), sizeMB, thresholdMB))
	}
	if clear {
		s.notifyAll(fmt.Sprintf("✅ %s disk back under threshold (%d MB)", s.serverName(serverID), sizeMB))
	}
}

// dirSizeMB returns the total size of a directory tree in whole megabytes
// (unreadable entries are skipped, not fatal).
func dirSizeMB(path string) int64 {
	var total int64
	filepath.Walk(path, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi != nil && !fi.IsDir() {
			total += fi.Size()
		}
		return nil
	})
	return total / (1024 * 1024)
}
