package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// A delegate is a user who has been granted some permissions on one specific
// server (RBAC scope_type='server'). These handlers give admins a server-centric
// view/editor that only touches that server's grants — a convenience wrapper
// over the per-user permission editor, scoped to a single server.

type delegate struct {
	UserID   string   `json:"user_id"`
	Username string   `json:"username"`
	Role     string   `json:"role"`
	Perms    []string `json:"perms"`
}

// handleListDelegates returns every user with a server-scoped grant on this server.
func (s *Server) handleListDelegates(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT p.user_id, u.username, u.role, p.perms
		   FROM permissions p JOIN users u ON u.id = p.user_id
		  WHERE p.scope_type='server' AND p.scope_id=?
		  ORDER BY u.username`, id)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	list := []delegate{}
	for rows.Next() {
		var d delegate
		var perms string
		if err := rows.Scan(&d.UserID, &d.Username, &d.Role, &perms); err != nil {
			continue
		}
		d.Perms = splitPerms(perms)
		list = append(list, d)
	}
	jsonOK(w, list)
}

// handleSetDelegates replaces the full set of server-scoped grants for this
// server. Each entry assigns one user a set of permissions on this server only;
// a user's grants at other scopes (or on other servers) are left untouched.
func (s *Server) handleSetDelegates(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var exists int
	if err := s.db.QueryRowContext(r.Context(), "SELECT 1 FROM servers WHERE id=?", id).Scan(&exists); err != nil {
		jsonError(w, "server not found", http.StatusNotFound)
		return
	}

	var req []delegate
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}

	// Validate users + permissions before mutating anything.
	for i := range req {
		if req[i].UserID == "" {
			jsonError(w, "user_id required", http.StatusBadRequest)
			return
		}
		var u int
		if err := s.db.QueryRowContext(r.Context(), "SELECT 1 FROM users WHERE id=?", req[i].UserID).Scan(&u); err != nil {
			jsonError(w, "unknown user: "+req[i].UserID, http.StatusBadRequest)
			return
		}
		for _, p := range req[i].Perms {
			if !rbac.Valid(rbac.Permission(p)) {
				jsonError(w, "invalid permission: "+p, http.StatusBadRequest)
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

	if _, err := tx.ExecContext(r.Context(),
		"DELETE FROM permissions WHERE scope_type='server' AND scope_id=?", id); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	for _, d := range req {
		if len(d.Perms) == 0 {
			continue
		}
		_, err := tx.ExecContext(r.Context(),
			"INSERT INTO permissions (id, user_id, scope_type, scope_id, perms) VALUES (?,?,?,?,?)",
			uuid.New().String(), d.UserID, string(rbac.ScopeServer), id, strings.Join(d.Perms, ","))
		if err != nil {
			jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}

	s.auditLog(r, "server.delegates", "server:"+id, map[string]int{"delegates": len(req)})
	jsonOK(w, map[string]string{"status": "updated"})
}

func splitPerms(s string) []string {
	if s == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
