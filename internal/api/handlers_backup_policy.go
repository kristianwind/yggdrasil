package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/scheduler"
)

// managedBackupPolicy marks the single, panel-wide nightly backup schedule so it
// is managed through the Settings "Backup policy" card rather than the generic
// Schedules list (which hides managed rows). It is a global schedule — server_id
// NULL — so scopeServers targets every server.
const managedBackupPolicy = "backup-policy"

type backupPolicyView struct {
	Enabled  bool   `json:"enabled"`
	Hour     int    `json:"hour"`      // 0-23, the daily run hour
	TargetID string `json:"target_id"` // required when enabled
}

// parseDailyHour pulls the hour out of a "M H * * *" daily cron, defaulting to 3.
func parseDailyHour(cron string) int {
	var m, h int
	if _, err := fmt.Sscanf(cron, "%d %d * * *", &m, &h); err == nil {
		return clampHour(h)
	}
	return 3
}

// handleGetBackupPolicy returns the panel-wide nightly backup policy, derived
// from the managed global schedule row (absent = disabled). Admin-only.
func (s *Server) handleGetBackupPolicy(w http.ResponseWriter, r *http.Request) {
	var cron, argsJSON string
	var enabled int
	err := s.db.QueryRowContext(r.Context(),
		"SELECT cron_expr, COALESCE(args_json,'{}'), enabled FROM schedules WHERE managed=? AND server_id IS NULL",
		managedBackupPolicy).Scan(&cron, &argsJSON, &enabled)
	if err != nil {
		jsonOK(w, backupPolicyView{Enabled: false, Hour: 3}) // sensible pre-fill
		return
	}
	var args map[string]string
	json.Unmarshal([]byte(argsJSON), &args)
	jsonOK(w, backupPolicyView{Enabled: enabled == 1, Hour: parseDailyHour(cron), TargetID: args["target_id"]})
}

// handleSetBackupPolicy creates/updates/removes the managed global backup
// schedule (all servers, nightly, to one target). Admin-only.
func (s *Server) handleSetBackupPolicy(w http.ResponseWriter, r *http.Request) {
	var req backupPolicyView
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}

	var existingID string
	s.db.QueryRowContext(r.Context(),
		"SELECT id FROM schedules WHERE managed=? AND server_id IS NULL", managedBackupPolicy).Scan(&existingID)

	if !req.Enabled {
		if existingID != "" {
			s.db.ExecContext(r.Context(), "DELETE FROM schedules WHERE id=?", existingID)
			s.auditLog(r, "backup.policy.off", "system", nil)
			s.reloadSchedules()
		}
		jsonOK(w, backupPolicyView{Enabled: false, Hour: req.Hour})
		return
	}
	if req.TargetID == "" {
		jsonError(w, "a backup target is required", http.StatusBadRequest)
		return
	}
	req.Hour = clampHour(req.Hour)
	cron := fmt.Sprintf("0 %d * * *", req.Hour)
	args := map[string]string{"target_id": req.TargetID}
	argsJSONBytes, _ := json.Marshal(args)
	name := fmt.Sprintf("Nightly backup — all servers (%02d:00)", req.Hour)

	if existingID != "" {
		s.db.ExecContext(r.Context(),
			"UPDATE schedules SET name=?, cron_expr=?, args_json=?, enabled=1 WHERE id=?",
			name, cron, string(argsJSONBytes), existingID)
	} else {
		s.db.ExecContext(r.Context(), `
			INSERT INTO schedules (id, name, server_id, cron_expr, action, args_json, enabled, managed)
			VALUES (?,?,NULL,?,?,?,1,?)`,
			uuid.New().String(), name, cron, string(scheduler.ActionBackup), string(argsJSONBytes), managedBackupPolicy)
	}
	s.auditLog(r, "backup.policy.on", "system", map[string]any{"hour": req.Hour, "target_id": req.TargetID})
	s.reloadSchedules()
	req.Enabled = true
	jsonOK(w, req)
}
