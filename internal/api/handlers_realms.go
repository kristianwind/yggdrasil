package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type realm struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	// ServerCount is admin-only: the list itself is readable by anyone signed in
	// (the create-server form needs it), but how much is in each realm is not
	// something a delegate needs from a form's dropdown.
	ServerCount *int `json:"server_count,omitempty"`
}

func (s *Server) handleListRealms(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT r.id, r.name, COALESCE(r.description,''), r.created_at, COUNT(sv.id)
		FROM realms r
		LEFT JOIN servers sv ON sv.realm_id = r.id
		GROUP BY r.id, r.name, r.description, r.created_at
		ORDER BY r.name`)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	admin := isAdmin(r)
	list := []realm{}
	for rows.Next() {
		var rl realm
		var count int
		if err := rows.Scan(&rl.ID, &rl.Name, &rl.Description, &rl.CreatedAt, &count); err != nil {
			continue
		}
		if admin {
			n := count
			rl.ServerCount = &n
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
		jsonError(w, realmWriteError(err, req.Name), realmWriteStatus(err))
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
	// Name is required here as it is on create. This writes both columns
	// unconditionally, so accepting an empty one meant a PUT carrying only a
	// description silently blanked the realm's name — and servers are matched to
	// realms by name (ensureRealm), so a nameless realm is one nothing can find.
	if err := decodeJSON(r, &req); err != nil || req.Name == "" {
		jsonError(w, "name required", http.StatusBadRequest)
		return
	}
	if _, err := s.db.ExecContext(r.Context(),
		"UPDATE realms SET name=?, description=? WHERE id=?",
		req.Name, req.Description, id); err != nil {
		jsonError(w, realmWriteError(err, req.Name), realmWriteStatus(err))
		return
	}
	s.auditLog(r, "realm.update", "realm:"+id, map[string]string{"name": req.Name})
	jsonOK(w, map[string]string{"status": "updated"})
}

// realms.name is UNIQUE, so a clash is a normal thing for a user to do — say what
// happened instead of returning a raw driver error as a 500.
func realmWriteError(err error, name string) string {
	if isUniqueViolation(err) {
		return "a realm named " + strconv.Quote(name) + " already exists"
	}
	return "db error: " + err.Error()
}

func realmWriteStatus(err error) int {
	if isUniqueViolation(err) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
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
