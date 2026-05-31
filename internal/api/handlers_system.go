package api

import (
	"net/http"
	"runtime"
	"strconv"
)

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

	jsonOK(w, map[string]interface{}{
		"docker_ok":       dockerOK,
		"servers":         serverCount,
		"servers_running": runningCount,
		"users":           userCount,
		"gameskills":      gameskillCount,
		"go_version":      runtime.Version(),
		"arch":            runtime.GOARCH,
	})
}
