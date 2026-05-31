package api

import (
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Violation rules are a global-admin feature (auto-actions on log patterns).

type violationView struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Pattern       string `json:"pattern"`
	Threshold     int    `json:"threshold"`
	WindowMinutes int    `json:"window_minutes"`
	Action        string `json:"action"`
	ScopeGlobal   bool   `json:"scope_global"`
	Enabled       bool   `json:"enabled"`
}

func (s *Server) handleListViolations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, name, pattern, threshold, window_minutes, action, scope_global, enabled FROM violation_rules ORDER BY name")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := []violationView{}
	for rows.Next() {
		var v violationView
		var scope, enabled int
		if rows.Scan(&v.ID, &v.Name, &v.Pattern, &v.Threshold, &v.WindowMinutes, &v.Action, &scope, &enabled) != nil {
			continue
		}
		v.ScopeGlobal = scope == 1
		v.Enabled = enabled == 1
		list = append(list, v)
	}
	jsonOK(w, list)
}

func (s *Server) handleCreateViolation(w http.ResponseWriter, r *http.Request) {
	var req violationView
	if err := decodeJSON(r, &req); err != nil || req.Name == "" || req.Pattern == "" {
		jsonError(w, "name and pattern required", http.StatusBadRequest)
		return
	}
	if _, err := regexp.Compile(req.Pattern); err != nil {
		jsonError(w, "invalid regex: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Action != "ban" && req.Action != "kick" {
		req.Action = "ban"
	}
	if req.Threshold < 1 {
		req.Threshold = 1
	}
	if req.WindowMinutes < 1 {
		req.WindowMinutes = 5
	}
	id := uuid.New().String()
	_, err := s.db.ExecContext(r.Context(), `
		INSERT INTO violation_rules (id, name, pattern, threshold, window_minutes, action, scope_global, enabled)
		VALUES (?,?,?,?,?,?,?,1)`,
		id, req.Name, req.Pattern, req.Threshold, req.WindowMinutes, req.Action, boolToInt(req.ScopeGlobal))
	if err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "violation.create", "rule:"+id, map[string]string{"name": req.Name, "action": req.Action})
	s.viol.reloadRules()
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{"id": id})
}

func (s *Server) handleUpdateViolation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Enabled != nil {
		s.db.ExecContext(r.Context(), "UPDATE violation_rules SET enabled=? WHERE id=?", boolToInt(*req.Enabled), id)
	}
	s.auditLog(r, "violation.update", "rule:"+id, nil)
	s.viol.reloadRules()
	jsonOK(w, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteViolation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.db.ExecContext(r.Context(), "DELETE FROM violation_rules WHERE id=?", id)
	s.auditLog(r, "violation.delete", "rule:"+id, nil)
	s.viol.reloadRules()
	jsonOK(w, map[string]string{"status": "deleted"})
}
