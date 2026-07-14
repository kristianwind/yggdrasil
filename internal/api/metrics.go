package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// Metrics history: a lightweight sampler records CPU%, memory and player count
// per running server every few minutes into the metrics table, pruned to a
// rolling 7-day window. It gives the panel real observability (history charts)
// beyond the point-in-time stats, and feeds trend context to the AI ops digest.

const (
	metricsInterval  = 5 * time.Minute
	metricsRetention = "-7 days"
)

func (s *Server) startMetricsSampler() {
	go func() {
		defer recoverLog("metricsSampler")
		s.sampleMetrics()
		t := time.NewTicker(metricsInterval)
		defer t.Stop()
		n := 0
		for range t.C {
			s.sampleMetrics()
			if n++; n%12 == 0 { // ~hourly
				s.db.Exec("DELETE FROM metrics WHERE ts < datetime('now', ?)", metricsRetention)
			}
		}
	}()
}

// sampleMetrics records one sample for every running server.
func (s *Server) sampleMetrics() {
	defer recoverLog("sampleMetrics")
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
		cpu, mem := 0.0, 0.0
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		if st, err := s.docker.GetStats(ctx, x.cid); err == nil {
			cpu, mem = st.CPUPercent, st.MemUsageMB
		}
		cancel()
		players := s.playersOnline(x.id) // -1 when the game has no query / is unreachable
		s.db.Exec("INSERT INTO metrics (server_id, cpu, mem_mb, players) VALUES (?,?,?,?)", x.id, cpu, mem, players)
		s.checkResourceAlarms(x.id, cpu, mem) // fire/clear per-server CPU/mem alarms
	}
}

type metricPoint struct {
	TS      string  `json:"ts"`
	CPU     float64 `json:"cpu"`
	MemMB   float64 `json:"mem_mb"`
	Players int     `json:"players"`
}

type quietHour struct {
	Hour       int     `json:"hour"`        // 0–23, server-local
	AvgPlayers float64 `json:"avg_players"` // rounded to 1 decimal
	Samples    int     `json:"samples"`
}

// handleQuietHours mines the sampled player counts to suggest the calmest time of
// day to run disruptive jobs (like a scheduled restart). It buckets the last 14
// days of samples by server-local hour and returns the average players per hour
// plus the quietest hour. No data (a game with no query, or a brand-new server)
// yields has_data=false so the UI can stay quiet rather than mislead.
func (s *Server) handleQuietHours(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerView, s.serverTarget(r.Context(), id)) {
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT CAST(strftime('%H', ts, 'localtime') AS INTEGER) AS h, AVG(players), COUNT(*)
		FROM metrics
		WHERE server_id=? AND players >= 0 AND ts >= datetime('now','-14 days')
		GROUP BY h ORDER BY h`, id)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	byHour := []quietHour{}
	recHour, recAvg := -1, 0.0
	for rows.Next() {
		var qh quietHour
		var avg float64
		if rows.Scan(&qh.Hour, &avg, &qh.Samples) != nil {
			continue
		}
		qh.AvgPlayers = float64(int(avg*10+0.5)) / 10
		byHour = append(byHour, qh)
		if recHour == -1 || avg < recAvg {
			recHour, recAvg = qh.Hour, avg
		}
	}
	jsonOK(w, map[string]any{
		"has_data":         recHour != -1,
		"recommended_hour": recHour,
		"recommended_avg":  float64(int(recAvg*10+0.5)) / 10,
		"by_hour":          byHour,
	})
}

// handleServerMetrics returns a server's samples over the last N hours (default
// 24, max 168 = 7 days).
func (s *Server) handleServerMetrics(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerView, s.serverTarget(r.Context(), id)) {
		return
	}
	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		if n, err := strconv.Atoi(h); err == nil && n > 0 && n <= 168 {
			hours = n
		}
	}
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT ts, cpu, mem_mb, players FROM metrics WHERE server_id=? AND ts >= datetime('now', ?) ORDER BY ts",
		id, fmt.Sprintf("-%d hours", hours))
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := []metricPoint{}
	for rows.Next() {
		var p metricPoint
		if rows.Scan(&p.TS, &p.CPU, &p.MemMB, &p.Players) == nil {
			list = append(list, p)
		}
	}
	jsonOK(w, list)
}
