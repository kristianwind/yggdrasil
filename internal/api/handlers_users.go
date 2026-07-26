package api

import (
	"fmt"
	"net/http"
	"net/mail"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/auth"
)

// minPasswordLen is the floor for panel account passwords. This store guards a
// root-equivalent system (the panel controls Docker), so a trivially guessable
// password here is a real risk.
const minPasswordLen = 12

// validatePassword enforces a minimum strength for a new/changed account
// password: long enough and not an obviously-weak/common value (weakSecret).
func validatePassword(pw string) error {
	if len([]rune(pw)) < minPasswordLen {
		return fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}
	if weakSecret(pw) {
		return fmt.Errorf("password is too common — choose a stronger one")
	}
	return nil
}

type userInfo struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Disabled  bool   `json:"disabled"`
	CreatedAt string `json:"created_at"`
}

// normalizeEmail trims an optional email and validates its shape. An empty
// string is allowed (email is optional — the account just can't self-reset).
func normalizeEmail(email string) (string, error) {
	e := strings.TrimSpace(email)
	if e == "" {
		return "", nil
	}
	if _, err := mail.ParseAddress(e); err != nil {
		return "", fmt.Errorf("invalid email address")
	}
	return e, nil
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, username, COALESCE(email,''), role, disabled, created_at FROM users ORDER BY username")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	list := []userInfo{}
	for rows.Next() {
		var u userInfo
		var disabled int
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Role, &disabled, &u.CreatedAt); err != nil {
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
		Email    string `json:"email"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Username == "" || req.Password == "" {
		jsonError(w, "username and password required", http.StatusBadRequest)
		return
	}
	email, err := normalizeEmail(req.Email)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Omitting the role means "user" — a safe default. Naming one we don't know is
	// a typo, and silently filing "administrator" as a plain user is the kind of
	// thing you only notice when the account can't do its job.
	if req.Role == "" {
		req.Role = "user"
	}
	if !validRole(req.Role) {
		jsonError(w, "role must be \"admin\" or \"user\"", http.StatusBadRequest)
		return
	}
	if err := validatePassword(req.Password); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		jsonError(w, "hash error", http.StatusInternalServerError)
		return
	}
	id := uuid.New().String()
	if _, err := s.db.ExecContext(r.Context(),
		"INSERT INTO users (id, username, password_hash, email, role) VALUES (?,?,?,?,?)",
		id, req.Username, hash, email, req.Role); err != nil {
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
		Email    *string `json:"email"`
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	// Validate before writing anything. This handler applies each field with its
	// own UPDATE, so rejecting halfway would leave the earlier ones applied — a
	// request that changed the password and mistyped the role would 400 while the
	// password had already changed.
	//
	// An unrecognised role used to fall through the condition below and do
	// nothing, while the request still returned 200, so a typo read as "promoted"
	// and wasn't.
	if req.Role != nil && !validRole(*req.Role) {
		jsonError(w, "role must be \"admin\" or \"user\"", http.StatusBadRequest)
		return
	}
	// Normalize/validate the email up front too, so a bad address is rejected
	// before any of the per-field UPDATEs below run.
	var normEmail string
	if req.Email != nil {
		e, err := normalizeEmail(*req.Email)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		normEmail = e
	}
	// Validate the password up front too, so a weak one is rejected before any of
	// this handler's per-field UPDATEs run.
	if req.Password != nil && *req.Password != "" {
		if err := validatePassword(*req.Password); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	if req.Password != nil && *req.Password != "" {
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			jsonError(w, "hash error", http.StatusInternalServerError)
			return
		}
		s.db.ExecContext(r.Context(), "UPDATE users SET password_hash=? WHERE id=?", hash, id)
	}
	if req.Email != nil {
		s.db.ExecContext(r.Context(), "UPDATE users SET email=? WHERE id=?", normEmail, id)
	}
	if req.Role != nil {
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

// validRole reports whether r is a role the panel understands. The two are a
// closed set: rbac scoping is what grants a non-admin anything, so there is no
// third tier to add here.
func validRole(r string) bool { return r == "admin" || r == "user" }
