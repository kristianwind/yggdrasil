package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/rbac"
	"github.com/kristianwind/yggdrasil/internal/scheduler"
)

type scheduleView struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	ServerID string            `json:"server_id,omitempty"`
	RealmID  string            `json:"realm_id,omitempty"`
	Cron     string            `json:"cron_expr"`
	Action   string            `json:"action"`
	Args     map[string]string `json:"args"`
	Enabled  bool              `json:"enabled"`
}

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	// Managed rows (auto-restart toggle etc.) are owned by a per-server control and
	// hidden here so they can't be hand-edited into an inconsistent state.
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, name, COALESCE(server_id,''), COALESCE(realm_id,''), cron_expr, action,
		        COALESCE(args_json,'{}'), enabled FROM schedules WHERE COALESCE(managed,'')='' ORDER BY name`)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	// Scan all rows and close the cursor before any further queries: modernc
	// SQLite runs with MaxOpenConns(1), so loadGrants/serverTarget while the
	// result set is open would deadlock.
	var all []scheduleView
	for rows.Next() {
		var v scheduleView
		var argsJSON string
		var enabled int
		if err := rows.Scan(&v.ID, &v.Name, &v.ServerID, &v.RealmID, &v.Cron, &v.Action, &argsJSON, &enabled); err != nil {
			continue
		}
		json.Unmarshal([]byte(argsJSON), &v.Args)
		v.Enabled = enabled == 1
		all = append(all, v)
	}
	rows.Close()

	admin := isAdmin(r)
	var grants []rbac.Grant
	if !admin {
		if c := claimsFromContext(r.Context()); c != nil {
			grants = s.loadGrants(r.Context(), c.UserID)
		}
	}

	list := []scheduleView{}
	for _, v := range all {
		if v.ServerID == "" {
			if admin { // realm/global schedules are admin-managed
				list = append(list, v)
			}
			continue
		}
		// Non-admins only see schedules for servers they can manage schedules on.
		if admin || rbac.Allowed(grants, rbac.ServerSchedule, s.serverTarget(r.Context(), v.ServerID)) {
			list = append(list, v)
		}
	}
	jsonOK(w, list)
}

// scheduleActionPerm maps a schedule action to the extra permission it requires
// beyond ServerSchedule, so a Schedule grant can't be escalated into Console,
// Control, or Backup capabilities. ActionMessage (a rendered player broadcast)
// needs nothing beyond Schedule.
func scheduleActionPerm(a scheduler.Action) (rbac.Permission, bool) {
	switch a {
	case scheduler.ActionCommand:
		return rbac.ServerConsole, true
	case scheduler.ActionStart, scheduler.ActionStop, scheduler.ActionRestart, scheduler.ActionUpdate:
		return rbac.ServerControl, true
	case scheduler.ActionBackup:
		return rbac.ServerBackup, true
	default:
		return "", false
	}
}

func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	var req scheduleView
	if err := decodeJSON(r, &req); err != nil || req.Name == "" || req.Cron == "" {
		jsonError(w, "name, cron_expr and action required", http.StatusBadRequest)
		return
	}
	if !scheduler.ValidAction(scheduler.Action(req.Action)) {
		jsonError(w, "invalid action: "+req.Action, http.StatusBadRequest)
		return
	}
	if err := scheduler.ValidateCron(req.Cron); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Authorize: server-scoped schedules need schedule perm; realm/global = admin.
	if req.ServerID != "" {
		if !s.can(w, r, rbac.ServerSchedule, s.serverTarget(r.Context(), req.ServerID)) {
			return
		}
		// A schedule must not let a Schedule-only delegate run actions they can't
		// trigger directly: require the action's own permission too (Command =>
		// Console, start/stop/restart/update => Control, backup => Backup).
		if !isAdmin(r) {
			if p, need := scheduleActionPerm(scheduler.Action(req.Action)); need &&
				!s.can(w, r, p, s.serverTarget(r.Context(), req.ServerID)) {
				return
			}
		}
	} else if !isAdmin(r) {
		jsonError(w, "only admins can create realm/global schedules", http.StatusForbidden)
		return
	}

	if req.Args == nil {
		req.Args = map[string]string{}
	}
	argsJSON, _ := json.Marshal(req.Args)
	id := uuid.New().String()
	enabled := 1 // new schedules start enabled; toggle off later if needed
	_, err := s.db.ExecContext(r.Context(), `
		INSERT INTO schedules (id, name, server_id, realm_id, cron_expr, action, args_json, enabled)
		VALUES (?,?,?,?,?,?,?,?)`,
		id, req.Name, nullableStr(req.ServerID), nullableStr(req.RealmID),
		req.Cron, req.Action, string(argsJSON), enabled)
	if err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "schedule.create", "schedule:"+id, map[string]string{"action": req.Action, "cron": req.Cron})
	s.reloadSchedules()
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{"id": id})
}

func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// All fields optional: a plain toggle sends {enabled}, while a full edit sends
	// name/cron/action/scope/args. Pointers let us tell "omitted" from "set empty".
	var req struct {
		Enabled  *bool              `json:"enabled"`
		Name     *string            `json:"name"`
		Cron     *string            `json:"cron_expr"`
		Action   *string            `json:"action"`
		ServerID *string            `json:"server_id"`
		RealmID  *string            `json:"realm_id"`
		Args     *map[string]string `json:"args"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !isAdmin(r) {
		jsonError(w, "admin required", http.StatusForbidden)
		return
	}
	var exists int
	if err := s.db.QueryRowContext(r.Context(), "SELECT 1 FROM schedules WHERE id=?", id).Scan(&exists); err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if req.Action != nil && !scheduler.ValidAction(scheduler.Action(*req.Action)) {
		jsonError(w, "invalid action: "+*req.Action, http.StatusBadRequest)
		return
	}
	if req.Cron != nil {
		if err := scheduler.ValidateCron(*req.Cron); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Enabled != nil {
		s.db.ExecContext(r.Context(), "UPDATE schedules SET enabled=? WHERE id=?", boolToInt(*req.Enabled), id)
	}
	if req.Name != nil {
		s.db.ExecContext(r.Context(), "UPDATE schedules SET name=? WHERE id=?", *req.Name, id)
	}
	if req.Cron != nil {
		s.db.ExecContext(r.Context(), "UPDATE schedules SET cron_expr=? WHERE id=?", *req.Cron, id)
	}
	if req.Action != nil {
		s.db.ExecContext(r.Context(), "UPDATE schedules SET action=? WHERE id=?", *req.Action, id)
	}
	if req.ServerID != nil {
		s.db.ExecContext(r.Context(), "UPDATE schedules SET server_id=? WHERE id=?", nullableStr(*req.ServerID), id)
	}
	if req.RealmID != nil {
		s.db.ExecContext(r.Context(), "UPDATE schedules SET realm_id=? WHERE id=?", nullableStr(*req.RealmID), id)
	}
	if req.Args != nil {
		argsJSON, _ := json.Marshal(*req.Args)
		s.db.ExecContext(r.Context(), "UPDATE schedules SET args_json=? WHERE id=?", string(argsJSON), id)
	}
	s.auditLog(r, "schedule.update", "schedule:"+id, nil)
	s.reloadSchedules()
	jsonOK(w, map[string]string{"status": "updated"})
}

// handleScheduleRuns returns the recent execution log for a schedule.
func (s *Server) handleScheduleRuns(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Authorize like the list: admin for realm/global, ServerSchedule for server-scoped.
	var serverID string
	if err := s.db.QueryRowContext(r.Context(), "SELECT COALESCE(server_id,'') FROM schedules WHERE id=?", id).Scan(&serverID); err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if serverID == "" {
		if !isAdmin(r) {
			jsonError(w, "admin required", http.StatusForbidden)
			return
		}
	} else if !s.can(w, r, rbac.ServerSchedule, s.serverTarget(r.Context(), serverID)) {
		return
	}
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT COALESCE(server_name,''), COALESCE(action,''), COALESCE(status,''), COALESCE(detail,''), ran_at
		 FROM schedule_runs WHERE schedule_id=? ORDER BY ran_at DESC, rowid DESC LIMIT 50`, id)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type runView struct {
		ServerName string `json:"server_name"`
		Action     string `json:"action"`
		Status     string `json:"status"`
		Detail     string `json:"detail"`
		RanAt      string `json:"ran_at"`
	}
	list := []runView{}
	for rows.Next() {
		var v runView
		if err := rows.Scan(&v.ServerName, &v.Action, &v.Status, &v.Detail, &v.RanAt); err != nil {
			continue
		}
		list = append(list, v)
	}
	jsonOK(w, list)
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !isAdmin(r) {
		jsonError(w, "admin required", http.StatusForbidden)
		return
	}
	s.db.ExecContext(r.Context(), "DELETE FROM schedules WHERE id=?", id)
	s.auditLog(r, "schedule.delete", "schedule:"+id, nil)
	s.reloadSchedules()
	jsonOK(w, map[string]string{"status": "deleted"})
}

// handleRunSchedule fires a schedule immediately (manual trigger).
func (s *Server) handleRunSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var exists int
	if err := s.db.QueryRowContext(r.Context(), "SELECT 1 FROM schedules WHERE id=?", id).Scan(&exists); err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !isAdmin(r) {
		jsonError(w, "admin required", http.StatusForbidden)
		return
	}
	s.auditLog(r, "schedule.run", "schedule:"+id, nil)
	go s.runScheduleByID(id)
	w.WriteHeader(http.StatusAccepted)
	jsonOK(w, map[string]string{"status": "triggered"})
}

// ---- Message templates ----

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, name, body, builtin FROM message_templates ORDER BY name")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type tmpl struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Body    string `json:"body"`
		Builtin bool   `json:"builtin"`
	}
	list := []tmpl{}
	for rows.Next() {
		var t tmpl
		var b int
		if err := rows.Scan(&t.ID, &t.Name, &t.Body, &b); err != nil {
			continue
		}
		t.Builtin = b == 1
		list = append(list, t)
	}
	jsonOK(w, list)
}

func (s *Server) handleSaveTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Body string `json:"body"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Name == "" || req.Body == "" {
		jsonError(w, "name and body required", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		req.ID = uuid.New().String()
		s.db.ExecContext(r.Context(),
			"INSERT INTO message_templates (id, name, body, builtin) VALUES (?,?,?,0)",
			req.ID, req.Name, req.Body)
	} else {
		s.db.ExecContext(r.Context(),
			"UPDATE message_templates SET name=?, body=? WHERE id=?", req.Name, req.Body, req.ID)
	}
	s.auditLog(r, "template.save", "template:"+req.ID, nil)
	jsonOK(w, map[string]string{"id": req.ID})
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.db.ExecContext(r.Context(), "DELETE FROM message_templates WHERE id=?", id)
	s.auditLog(r, "template.delete", "template:"+id, nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
