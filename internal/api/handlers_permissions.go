package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// handlePermissionsCatalog returns the assignable permissions and scope types so
// the UI can render an editor without hardcoding them.
func (s *Server) handlePermissionsCatalog(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"permissions": rbac.All,
		"scope_types": []rbac.ScopeType{rbac.ScopeGlobal, rbac.ScopeRealm, rbac.ScopeGameskill, rbac.ScopeServer},
	})
}

// handleGetUserPermissions returns a user's grants.
func (s *Server) handleGetUserPermissions(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	grants := s.loadGrants(r.Context(), userID)
	if grants == nil {
		grants = []rbac.Grant{}
	}
	jsonOK(w, grants)
}

// handleSetUserPermissions replaces a user's grants with the provided set.
func (s *Server) handleSetUserPermissions(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")

	// Confirm the user exists.
	var exists int
	if err := s.db.QueryRowContext(r.Context(), "SELECT 1 FROM users WHERE id=?", userID).Scan(&exists); err != nil {
		jsonError(w, "user not found", http.StatusNotFound)
		return
	}

	var grants []rbac.Grant
	if err := decodeJSON(r, &grants); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}

	// Validate before mutating.
	for _, g := range grants {
		switch g.ScopeType {
		case rbac.ScopeGlobal, rbac.ScopeRealm, rbac.ScopeGameskill, rbac.ScopeServer:
		default:
			jsonError(w, "invalid scope_type: "+string(g.ScopeType), http.StatusBadRequest)
			return
		}
		if g.ScopeType != rbac.ScopeGlobal && g.ScopeID == "" {
			jsonError(w, "scope_id required for non-global scope", http.StatusBadRequest)
			return
		}
		for _, p := range g.Perms {
			if !rbac.Valid(p) {
				jsonError(w, "invalid permission: "+string(p), http.StatusBadRequest)
				return
			}
		}
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(r.Context(), "DELETE FROM permissions WHERE user_id=?", userID); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	for _, g := range grants {
		if len(g.Perms) == 0 {
			continue
		}
		perms := make([]string, 0, len(g.Perms))
		for _, p := range g.Perms {
			perms = append(perms, string(p))
		}
		_, err := tx.ExecContext(r.Context(),
			"INSERT INTO permissions (id, user_id, scope_type, scope_id, perms) VALUES (?,?,?,?,?)",
			uuid.New().String(), userID, string(g.ScopeType), nullableStr(g.ScopeID), strings.Join(perms, ","))
		if err != nil {
			jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}

	s.auditLog(r, "user.permissions", "user:"+userID, map[string]int{"grants": len(grants)})
	jsonOK(w, map[string]string{"status": "updated"})
}
