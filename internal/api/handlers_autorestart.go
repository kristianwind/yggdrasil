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
	AnchorHour  int    `json:"anchor_hour"`  // 0-23, the hour the cycle starts from
	Warn        bool   `json:"warn"`         // broadcast the rune's countdown before restarting
	BackupFirst bool   `json:"backup_first"` // take a safety backup first
	TargetID    string `json:"target_id"`    // backup target (required if backup_first)
}

// autoRestartCron builds the cron expression for "every N hours, starting at
// anchor" (minute 0). N is clamped to 1..24; 24 means once a day, at the anchor.
//
// The anchor is what makes the quiet-hours recommendation actionable: "every 6
// hours" alone always fires at 00/06/12/18, which is no help if your server is
// busiest at 18:00. `0 3-23/6 * * *` fires at 03/09/15/21 instead.
//
// Anchor 0 keeps emitting the plain `*/N` form it always has, so enabling the
// feature doesn't rewrite every existing server's schedule row.
//
// Caveat worth knowing: when N doesn't divide 24 evenly the day wraps early —
// `0 3-23/5` fires 03/08/13/18/23 and then 03 again, a 4h gap rather than 5. Cron
// has no notion of a cycle crossing midnight, so this is inherent, not a bug.
func autoRestartCron(everyHours, anchorHour int) string {
	if everyHours < 1 {
		everyHours = 1
	}
	anchorHour = clampHour(anchorHour)
	if everyHours >= 24 {
		return fmt.Sprintf("0 %d * * *", anchorHour)
	}
	if anchorHour == 0 {
		return fmt.Sprintf("0 */%d * * *", everyHours)
	}
	return fmt.Sprintf("0 %d-23/%d * * *", anchorHour, everyHours)
}

func clampHour(h int) int {
	if h < 0 || h > 23 {
		return 0
	}
	return h
}

// parseAutoRestart recovers (N, anchor) from the cron shapes autoRestartCron
// emits. Falls back to (6, 0) for anything we don't recognise, so a hand-edited
// row degrades to a sane default rather than a confusing one.
func parseAutoRestart(expr string) (everyHours, anchorHour int) {
	f := strings.Fields(expr)
	if len(f) < 2 {
		return 6, 0
	}
	hour := f[1]

	// "0 A-23/N * * *" — every N hours from A.
	if a, rest, ok := strings.Cut(hour, "-"); ok {
		if _, n, ok := strings.Cut(rest, "/"); ok {
			ai, err1 := strconv.Atoi(a)
			ni, err2 := strconv.Atoi(n)
			if err1 == nil && err2 == nil && ni > 0 {
				return ni, clampHour(ai)
			}
		}
		return 6, 0
	}
	// "0 */N * * *" — every N hours from midnight.
	if n, ok := strings.CutPrefix(hour, "*/"); ok {
		if ni, err := strconv.Atoi(n); err == nil && ni > 0 {
			return ni, 0
		}
		return 6, 0
	}
	// "0 A * * *" — once a day at A.
	if ai, err := strconv.Atoi(hour); err == nil {
		return 24, clampHour(ai)
	}
	return 6, 0
}

// autoRestartName is the schedule row's display name. Managed rows are hidden
// from the Schedules page, but the name still surfaces in the audit log and in
// the run history, so it should read like what the operator chose.
func autoRestartName(everyHours, anchorHour int) string {
	if everyHours >= 24 {
		return fmt.Sprintf("Auto-restart daily at %02d:00", clampHour(anchorHour))
	}
	if anchorHour == 0 {
		return fmt.Sprintf("Auto-restart every %dh", everyHours)
	}
	return fmt.Sprintf("Auto-restart every %dh from %02d:00", everyHours, clampHour(anchorHour))
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
	every, anchor := parseAutoRestart(cron)
	jsonOK(w, autoRestartView{
		Enabled:     enabled == 1,
		EveryHours:  every,
		AnchorHour:  anchor,
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
	req.AnchorHour = clampHour(req.AnchorHour)
	cron := autoRestartCron(req.EveryHours, req.AnchorHour)
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
	name := autoRestartName(req.EveryHours, req.AnchorHour)

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
	s.auditLog(r, "server.auto_restart.on", "server:"+id,
		map[string]any{"every_hours": req.EveryHours, "anchor_hour": req.AnchorHour, "warn": req.Warn})
	s.reloadSchedules()
	req.Enabled = true
	jsonOK(w, req)
}
