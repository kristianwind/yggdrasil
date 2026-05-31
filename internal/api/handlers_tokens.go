package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/auth"
)

// API tokens let external automation (e.g. a home AI) drive the panel as the
// owning user. The plaintext token is shown once on creation; only its hash is
// stored. A token inherits its owner's role/permissions.

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, name, COALESCE(last_used_at,''), created_at FROM api_tokens WHERE user_id=? ORDER BY created_at DESC",
		claims.UserID)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type tok struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		LastUsedAt string `json:"last_used_at,omitempty"`
		CreatedAt  string `json:"created_at"`
	}
	list := []tok{}
	for rows.Next() {
		var t tok
		if err := rows.Scan(&t.ID, &t.Name, &t.LastUsedAt, &t.CreatedAt); err != nil {
			continue
		}
		list = append(list, t)
	}
	jsonOK(w, list)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Name == "" {
		jsonError(w, "name required", http.StatusBadRequest)
		return
	}
	token, hash, err := auth.GenerateAPIToken()
	if err != nil {
		jsonError(w, "token gen error", http.StatusInternalServerError)
		return
	}
	id := uuid.New().String()
	if _, err := s.db.ExecContext(r.Context(),
		"INSERT INTO api_tokens (id, user_id, name, token_hash) VALUES (?,?,?,?)",
		id, claims.UserID, req.Name, hash); err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "token.create", "token:"+id, map[string]string{"name": req.Name})
	w.WriteHeader(http.StatusCreated)
	// The plaintext token is returned exactly once.
	jsonOK(w, map[string]string{"id": id, "name": req.Name, "token": token})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	id := chi.URLParam(r, "id")
	// Users may only delete their own tokens.
	res, err := s.db.ExecContext(r.Context(),
		"DELETE FROM api_tokens WHERE id=? AND user_id=?", id, claims.UserID)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	s.auditLog(r, "token.delete", "token:"+id, nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}
