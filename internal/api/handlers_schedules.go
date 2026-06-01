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
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, name, COALESCE(server_id,''), COALESCE(realm_id,''), cron_expr, action,
		        COALESCE(args_json,'{}'), enabled FROM schedules ORDER BY name`)
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
	var req struct {
		Enabled *bool   `json:"enabled"`
		Cron    *string `json:"cron_expr"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !isAdmin(r) {
		jsonError(w, "admin required", http.StatusForbidden)
		return
	}
	if req.Enabled != nil {
		s.db.ExecContext(r.Context(), "UPDATE schedules SET enabled=? WHERE id=?", boolToInt(*req.Enabled), id)
	}
	if req.Cron != nil {
		if err := scheduler.ValidateCron(*req.Cron); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.db.ExecContext(r.Context(), "UPDATE schedules SET cron_expr=? WHERE id=?", *req.Cron, id)
	}
	s.auditLog(r, "schedule.update", "schedule:"+id, nil)
	s.reloadSchedules()
	jsonOK(w, map[string]string{"status": "updated"})
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
