package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type realm struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
}

func (s *Server) handleListRealms(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, name, COALESCE(description,''), created_at FROM realms ORDER BY name")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	list := []realm{}
	for rows.Next() {
		var rl realm
		if err := rows.Scan(&rl.ID, &rl.Name, &rl.Description, &rl.CreatedAt); err != nil {
			continue
		}
		list = append(list, rl)
	}
	jsonOK(w, list)
}

func (s *Server) handleCreateRealm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Name == "" {
		jsonError(w, "name required", http.StatusBadRequest)
		return
	}
	id := uuid.New().String()
	if _, err := s.db.ExecContext(r.Context(),
		"INSERT INTO realms (id, name, description) VALUES (?,?,?)",
		id, req.Name, req.Description); err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "realm.create", "realm:"+id, map[string]string{"name": req.Name})
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{"id": id})
}

func (s *Server) handleUpdateRealm(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if _, err := s.db.ExecContext(r.Context(),
		"UPDATE realms SET name=?, description=? WHERE id=?",
		req.Name, req.Description, id); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "realm.update", "realm:"+id, nil)
	jsonOK(w, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteRealm(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Detach servers from this realm rather than cascade-deleting them.
	s.db.ExecContext(r.Context(), "UPDATE servers SET realm_id=NULL WHERE realm_id=?", id)
	if _, err := s.db.ExecContext(r.Context(), "DELETE FROM realms WHERE id=?", id); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "realm.delete", "realm:"+id, nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}
