package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/rbac"
	"github.com/kristianwind/yggdrasil/internal/scheduler"
)

// Auto-restart is a per-server convenience toggle ("restart every N hours") that
// is backed by an ordinary managed schedule row (managed='auto-restart'). The
// toggle owns that row's whole lifecycle — create on enable, update on change,
// delete on disable — so the user never hand-edits cron. Managed rows are hidden
// from the generic schedule list; this endpoint is their only control surface.
//
// It reuses the existing restart action (with the rune's warnings + optional
// backup), so it's just a friendly front door onto the scheduler, not new timing
// machinery — consistent with the "timing lives in Schedules" architecture.
const managedAutoRestart = "auto-restart"

type autoRestartView struct {
	Enabled     bool   `json:"enabled"`
	EveryHours  int    `json:"every_hours"`
	Warn        bool   `json:"warn"`         // broadcast the rune's countdown before restarting
	BackupFirst bool   `json:"backup_first"` // take a safety backup first
	TargetID    string `json:"target_id"`    // backup target (required if backup_first)
}

// autoRestartCron builds the cron expression for "every N hours" (minute 0).
// N is clamped to 1..24; 24 collapses to a plain daily 0 0 * * *.
func autoRestartCron(everyHours int) string {
	if everyHours < 1 {
		everyHours = 1
	}
	if everyHours >= 24 {
		return "0 0 * * *"
	}
	return fmt.Sprintf("0 */%d * * *", everyHours)
}

// parseAutoRestartHours recovers N from a "0 */N * * *" / "0 0 * * *" cron. Falls
// back to 6 if the expression was edited into a shape we don't recognize.
func parseAutoRestartHours(expr string) int {
	f := strings.Fields(expr)
	if len(f) < 2 {
		return 6
	}
	hour := f[1]
	if hour == "0" {
		return 24
	}
	if strings.HasPrefix(hour, "*/") {
		if n, err := strconv.Atoi(strings.TrimPrefix(hour, "*/")); err == nil && n > 0 {
			return n
		}
	}
	return 6
}

// handleGetAutoRestart returns the current auto-restart toggle state, derived
// from the server's managed schedule row (absent = disabled).
func (s *Server) handleGetAutoRestart(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerControl, s.serverTarget(r.Context(), id)) {
		return
	}
	var cron, argsJSON string
	var enabled int
	err := s.db.QueryRowContext(r.Context(),
		"SELECT cron_expr, COALESCE(args_json,'{}'), enabled FROM schedules WHERE server_id=? AND managed=?",
		id, managedAutoRestart).Scan(&cron, &argsJSON, &enabled)
	if err != nil {
		// No managed row yet — report a sensible default the UI can pre-fill.
		jsonOK(w, autoRestartView{Enabled: false, EveryHours: 6, Warn: true, BackupFirst: false})
		return
	}
	var args map[string]string
	json.Unmarshal([]byte(argsJSON), &args)
	jsonOK(w, autoRestartView{
		Enabled:     enabled == 1,
		EveryHours:  parseAutoRestartHours(cron),
		Warn:        argTrue(args["warn"]),
		BackupFirst: argTrue(args["backup_first"]),
		TargetID:    args["target_id"],
	})
}

// handleSetAutoRestart creates/updates/removes the managed auto-restart schedule.
func (s *Server) handleSetAutoRestart(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerControl, srv.target()) {
		return
	}
	var req autoRestartView
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.BackupFirst && req.TargetID == "" {
		jsonError(w, "backup-first requires a backup target", http.StatusBadRequest)
		return
	}

	// Find any existing managed row for this server.
	var existingID string
	s.db.QueryRowContext(r.Context(),
		"SELECT id FROM schedules WHERE server_id=? AND managed=?", id, managedAutoRestart).Scan(&existingID)

	if !req.Enabled {
		if existingID != "" {
			s.db.ExecContext(r.Context(), "DELETE FROM schedules WHERE id=?", existingID)
			s.auditLog(r, "server.auto_restart.off", "server:"+id, nil)
			s.reloadSchedules()
		}
		jsonOK(w, autoRestartView{Enabled: false, EveryHours: req.EveryHours})
		return
	}

	if req.EveryHours < 1 {
		req.EveryHours = 6
	}
	cron := autoRestartCron(req.EveryHours)
	// Schedule args are "true"/"false" — that's what runAction reads and what the
	// Schedules page writes. boolStr yields "1"/"0", which is the app_settings
	// convention and silently means false here.
	args := map[string]string{
		"warn":         strconv.FormatBool(req.Warn),
		"backup_first": strconv.FormatBool(req.BackupFirst),
		"target_id":    req.TargetID,
	}
	argsJSONBytes, _ := json.Marshal(args)
	argsJSON := string(argsJSONBytes)
	name := fmt.Sprintf("Auto-restart every %dh", req.EveryHours)

	if existingID != "" {
		s.db.ExecContext(r.Context(),
			"UPDATE schedules SET name=?, cron_expr=?, args_json=?, enabled=1 WHERE id=?",
			name, cron, argsJSON, existingID)
	} else {
		s.db.ExecContext(r.Context(), `
			INSERT INTO schedules (id, name, server_id, cron_expr, action, args_json, enabled, managed)
			VALUES (?,?,?,?,?,?,1,?)`,
			uuid.New().String(), name, id, cron, string(scheduler.ActionRestart), argsJSON, managedAutoRestart)
	}
	s.auditLog(r, "server.auto_restart.on", "server:"+id, map[string]any{"every_hours": req.EveryHours, "warn": req.Warn})
	s.reloadSchedules()
	req.Enabled = true
	jsonOK(w, req)
}
