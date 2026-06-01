package api

import (
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

// diskUsage returns free and total bytes for the filesystem holding path.
// Works on Linux (production) and darwin (dev): Bsize differs in type, hence the
// explicit conversion.
func diskUsage(path string) (free, total uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bsize := uint64(st.Bsize)
	return st.Bavail * bsize, st.Blocks * bsize
}

// startDiskMonitor periodically checks free disk on the data filesystem and
// sends one notification when it crosses below 10%, re-arming once it recovers.
func (s *Server) startDiskMonitor() {
	go func() {
		path := filepath.Dir(s.cfg.Database.Path)
		alerted := false
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		check := func() {
			free, total := diskUsage(path)
			if total == 0 {
				return
			}
			pct := float64(free) / float64(total) * 100
			if pct < 10 && !alerted {
				alerted = true
				s.notifyAll("⚠️ Low disk space: " + strconv.FormatFloat(pct, 'f', 1, 64) +
					"% free on the Yggdrasil data volume.")
			} else if pct >= 15 {
				alerted = false
			}
		}
		check()
		for range t.C {
			check()
		}
	}()
}

type auditEntry struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Detail   string `json:"detail"`
	IP       string `json:"ip"`
	TS       string `json:"ts"`
}

func (s *Server) handleAuditLog(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, COALESCE(username,''), action, COALESCE(resource,''),
		        COALESCE(detail_json,''), COALESCE(ip,''), ts
		 FROM audit_log ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	list := []auditEntry{}
	for rows.Next() {
		var e auditEntry
		if err := rows.Scan(&e.ID, &e.Username, &e.Action, &e.Resource, &e.Detail, &e.IP, &e.TS); err != nil {
			continue
		}
		list = append(list, e)
	}
	jsonOK(w, list)
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	dockerOK := s.docker.Ping(r.Context()) == nil

	var serverCount, runningCount, userCount, gameskillCount int
	s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM servers").Scan(&serverCount)
	s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM servers WHERE status='running'").Scan(&runningCount)
	s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM users").Scan(&userCount)
	s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM gameskills").Scan(&gameskillCount)

	free, total := diskUsage(filepath.Dir(s.cfg.Database.Path))
	memTotal, memUsed := hostMem()
	cpuPct := hostCPUPercent()

	jsonOK(w, map[string]interface{}{
		"docker_ok":        dockerOK,
		"servers":          serverCount,
		"servers_running":  runningCount,
		"users":            userCount,
		"gameskills":       gameskillCount,
		"go_version":       runtime.Version(),
		"arch":             runtime.GOARCH,
		"cpu_count":        runtime.NumCPU(),
		"cpu_percent":      cpuPct, // -1 when unavailable (e.g. non-Linux)
		"mem_total_bytes":  memTotal,
		"mem_used_bytes":   memUsed,
		"disk_free_bytes":  free,
		"disk_total_bytes": total,
	})
}
