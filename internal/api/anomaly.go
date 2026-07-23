package api

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// Player/traffic anomaly detection — the Kvasir layer that notices *activity*
// being wrong, not just log lines matching a pattern. Two deterministic
// detectors run on a fixed tick, and each finding is both notified as-is and
// (when the AI is configured) handed to kvasirReact for an explanation:
//
//   - players: a mass disconnect (most of the players gone between two samples
//     while the server kept running) or an influx above the 14-day high — the
//     "players misbehaving / raid / login storm" signals a count can carry.
//   - log rate: the container suddenly logging far more than its own recent
//     baseline (kept as an in-memory EMA per server). Catches a WordPress
//     traffic spike, a failed-login flood or an error storm without anyone
//     having written a regex for it.
//
// Everything here is opt-in via the 'anomaly' proactive trigger (Settings →
// Kvasir), and deliberately conservative: absolute floors, a warm-up before the
// rate baseline is trusted, and a per-server-per-kind cooldown.

const (
	anomalyInterval       = 5 * time.Minute
	anomalyCooldown       = time.Hour
	anomalyPlayerDropMin  = 4   // a drop only counts from at least this many players
	anomalyPlayerSpikeMin = 8   // an influx only counts at/above this many players
	anomalyLogFloor       = 100 // lines per tick below which a spike is never called
	anomalyLogFactor      = 5.0 // recent rate must exceed baseline by this factor
	anomalyLogWarmup      = 6   // EMA samples needed before rate alerts arm (~30 min)
)

type anomalyState struct {
	mu        sync.Mutex
	lastFired map[string]time.Time // "serverID|kind" → last alert
	logEMA    map[string]float64   // serverID → lines-per-tick baseline
	logSeen   map[string]int       // serverID → samples folded into the EMA
}

func newAnomalyState() *anomalyState {
	return &anomalyState{lastFired: map[string]time.Time{}, logEMA: map[string]float64{}, logSeen: map[string]int{}}
}

// coolingDown records the firing when it answers false, so callers alert at
// most once per cooldown per (server, kind).
func (a *anomalyState) coolingDown(serverID, kind string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := serverID + "|" + kind
	if t, ok := a.lastFired[key]; ok && now.Sub(t) < anomalyCooldown {
		return true
	}
	a.lastFired[key] = now
	return false
}

// foldLogRate returns the pre-update baseline and sample count, then folds the
// new observation into the EMA — the current tick never judges itself.
func (a *anomalyState) foldLogRate(serverID string, count int) (ema float64, seen int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ema, seen = a.logEMA[serverID], a.logSeen[serverID]
	if seen == 0 {
		a.logEMA[serverID] = float64(count)
	} else {
		a.logEMA[serverID] = 0.7*a.logEMA[serverID] + 0.3*float64(count)
	}
	a.logSeen[serverID] = seen + 1
	return ema, seen
}

// detectPlayerAnomaly compares the two most recent player samples plus the
// 14-day high. Pure — the thresholds are the whole policy.
func detectPlayerAnomaly(prev, cur, histMax int) (kind, detail string) {
	if prev >= anomalyPlayerDropMin && cur*4 <= prev {
		return "player-drop", fmt.Sprintf(
			"players dropped %d → %d between samples (~5 min) while the server kept running", prev, cur)
	}
	if cur >= anomalyPlayerSpikeMin && histMax >= 0 && cur > histMax {
		return "player-spike", fmt.Sprintf(
			"%d players online — above the previous 14-day high of %d", cur, histMax)
	}
	return "", ""
}

// detectLogRateSpike judges one tick's line count against the server's own
// baseline. Pure; ema/seen come from foldLogRate's pre-update return.
func detectLogRateSpike(count int, ema float64, seen int) (detail string, ok bool) {
	if seen < anomalyLogWarmup || count < anomalyLogFloor {
		return "", false
	}
	if float64(count) >= anomalyLogFactor*math.Max(ema, 1) {
		return fmt.Sprintf(
			"log/traffic volume spike: %d log lines in the last 5 minutes vs a typical ~%.0f", count, ema), true
	}
	return "", false
}

func (s *Server) startAnomalyLoop() {
	go func() {
		defer recoverLog("anomalyLoop")
		t := time.NewTicker(anomalyInterval)
		defer t.Stop()
		for range t.C {
			s.scanAnomalies()
		}
	}()
}

func (s *Server) scanAnomalies() {
	defer recoverLog("scanAnomalies")
	cfg := s.loadAIConfig(context.Background())
	if !kvasirTriggerOn(cfg.ProactiveTriggers, "anomaly") {
		return // explicit opt-in only: the default trigger list doesn't include it
	}
	rows, err := s.db.Query("SELECT id, COALESCE(container_id,'') FROM servers WHERE status='running' AND container_id<>''")
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
		s.checkPlayerAnomaly(x.id, x.cid)
		s.checkLogRateAnomaly(x.id, x.cid)
	}
}

func (s *Server) checkPlayerAnomaly(serverID, containerID string) {
	var cur, prev int
	rows, err := s.db.Query(
		`SELECT players FROM metrics WHERE server_id=? AND players>=0
		 AND ts >= datetime('now','-12 minutes') ORDER BY ts DESC LIMIT 2`, serverID)
	if err != nil {
		return
	}
	n := 0
	for rows.Next() {
		v := 0
		if rows.Scan(&v) == nil {
			if n == 0 {
				cur = v
			} else {
				prev = v
			}
			n++
		}
	}
	rows.Close()
	if n < 2 {
		return // needs two fresh samples — also skips query-less runes (players=-1)
	}
	histMax := -1
	s.db.QueryRow(
		`SELECT COALESCE(MAX(players),-1) FROM metrics WHERE server_id=? AND players>=0
		 AND ts >= datetime('now','-14 days') AND ts < datetime('now','-15 minutes')`, serverID).
		Scan(&histMax) //nolint:errcheck
	kind, detail := detectPlayerAnomaly(prev, cur, histMax)
	if kind == "" {
		return
	}
	s.fireAnomaly(serverID, containerID, kind, detail)
}

func (s *Server) checkLogRateAnomaly(serverID, containerID string) {
	lines := s.watcherLogLines(containerID, "5m")
	count := 0
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			count++
		}
	}
	ema, seen := s.anomalies.foldLogRate(serverID, count)
	detail, ok := detectLogRateSpike(count, ema, seen)
	if !ok {
		return
	}
	s.fireAnomaly(serverID, containerID, "log-rate", detail)
}

func (s *Server) fireAnomaly(serverID, containerID, kind, detail string) {
	if s.anomalies.coolingDown(serverID, kind, time.Now()) {
		return
	}
	name := s.serverName(serverID)
	// The deterministic note goes out regardless of the AI: the detection is
	// fact, only the explanation needs a model.
	s.notifyServer(serverID, fmt.Sprintf("📈 Anomaly · **%s** — %s", name, detail))
	tail := s.watcherLogLines(containerID, "10m")
	if len(tail) > 20 {
		tail = tail[len(tail)-20:]
	}
	for i, l := range tail {
		tail[i] = stripANSI(l)
	}
	go s.kvasirReact(serverID, "anomaly", detail, strings.Join(tail, "\n"))
}
