package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/auth"
)

type userInfo struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Disabled  bool   `json:"disabled"`
	CreatedAt string `json:"created_at"`
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, username, role, disabled, created_at FROM users ORDER BY username")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	list := []userInfo{}
	for rows.Next() {
		var u userInfo
		var disabled int
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &disabled, &u.CreatedAt); err != nil {
			continue
		}
		u.Disabled = disabled == 1
		list = append(list, u)
	}
	jsonOK(w, list)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Username == "" || req.Password == "" {
		jsonError(w, "username and password required", http.StatusBadRequest)
		return
	}
	if req.Role != "admin" && req.Role != "user" {
		req.Role = "user"
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		jsonError(w, "hash error", http.StatusInternalServerError)
		return
	}
	id := uuid.New().String()
	if _, err := s.db.ExecContext(r.Context(),
		"INSERT INTO users (id, username, password_hash, role) VALUES (?,?,?,?)",
		id, req.Username, hash, req.Role); err != nil {
		jsonError(w, "db error (username taken?): "+err.Error(), http.StatusBadRequest)
		return
	}
	s.auditLog(r, "user.create", "user:"+id, map[string]string{"username": req.Username, "role": req.Role})
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{"id": id})
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Password *string `json:"password"`
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Password != nil && *req.Password != "" {
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			jsonError(w, "hash error", http.StatusInternalServerError)
			return
		}
		s.db.ExecContext(r.Context(), "UPDATE users SET password_hash=? WHERE id=?", hash, id)
	}
	if req.Role != nil && (*req.Role == "admin" || *req.Role == "user") {
		s.db.ExecContext(r.Context(), "UPDATE users SET role=? WHERE id=?", *req.Role, id)
	}
	if req.Disabled != nil {
		d := 0
		if *req.Disabled {
			d = 1
		}
		s.db.ExecContext(r.Context(), "UPDATE users SET disabled=? WHERE id=?", d, id)
	}
	// Any of password / role / disabled changing must revoke the user's existing
	// sessions (a demoted, disabled, or password-reset user shouldn't keep access).
	if (req.Password != nil && *req.Password != "") || req.Role != nil || req.Disabled != nil {
		s.db.ExecContext(r.Context(), "UPDATE users SET token_version = COALESCE(token_version,0)+1 WHERE id=?", id)
	}
	s.auditLog(r, "user.update", "user:"+id, nil)
	jsonOK(w, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	claims := claimsFromContext(r.Context())
	if claims != nil && claims.UserID == id {
		jsonError(w, "cannot delete yourself", http.StatusBadRequest)
		return
	}
	// Refuse to delete the last admin.
	var adminCount int
	s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0").Scan(&adminCount)
	var targetRole string
	s.db.QueryRowContext(r.Context(), "SELECT role FROM users WHERE id=?", id).Scan(&targetRole)
	if targetRole == "admin" && adminCount <= 1 {
		jsonError(w, "cannot delete the last admin", http.StatusBadRequest)
		return
	}
	if _, err := s.db.ExecContext(r.Context(), "DELETE FROM users WHERE id=?", id); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "user.delete", "user:"+id, nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}
