package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

func (s *Server) handleListGameskills(w http.ResponseWriter, r *http.Request) {
	// Load the caller's grants first (its cursor opens+closes fully) — doing it
	// while the gameskills cursor below is open would deadlock the single-conn
	// modernc pool. `creatable` lets the UI show only the runes a delegated user
	// may actually create a server from (admins can create any).
	admin := isAdmin(r)
	var grants []rbac.Grant
	if !admin {
		if c := claimsFromContext(r.Context()); c != nil {
			grants = s.loadGrants(r.Context(), c.UserID)
		}
	}

	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, name, category, version, builtin FROM gameskills ORDER BY name")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type item struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Category  string `json:"category"`
		Version   int    `json:"version"`
		Builtin   bool   `json:"builtin"`
		Creatable bool   `json:"creatable"` // caller may create a server of this rune type
	}
	var list []item
	for rows.Next() {
		var it item
		var builtin int
		if err := rows.Scan(&it.ID, &it.Name, &it.Category, &it.Version, &builtin); err != nil {
			continue
		}
		it.Builtin = builtin == 1
		// Mirror the create endpoint's check: ServerCreate against a gameskill-only
		// target (the create UI picks no realm), so a global or gameskill-scoped
		// grant qualifies — matching exactly what POST /api/servers will allow.
		it.Creatable = admin || rbac.Allowed(grants, rbac.ServerCreate, rbac.Target{GameskillID: it.ID})
		list = append(list, it)
	}
	if list == nil {
		list = []item{}
	}
	jsonOK(w, list)
}

func (s *Server) handleGetGameskill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var yamlBlob string
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT yaml_blob FROM gameskills WHERE id=?", id).Scan(&yamlBlob); err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	gs, err := gameskill.Parse([]byte(yamlBlob))
	if err != nil {
		jsonError(w, "parse error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, gs)
}

func (s *Server) handleUploadGameskill(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 512*1024) // 512 KB max
	data, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	gs, err := gameskill.Parse(data)
	if err != nil {
		jsonError(w, "invalid gameskill: "+err.Error(), http.StatusBadRequest)
		return
	}

	id := gs.ID
	if id == "" {
		id = uuid.New().String()
	}

	// Never let an uploaded rune overwrite a built-in one — that would let someone
	// backdoor a rune already in use by running servers.
	if s.isBuiltinRune(r.Context(), id) {
		jsonError(w, "cannot overwrite a built-in rune; use a different id", http.StatusConflict)
		return
	}

	_, err = s.db.ExecContext(r.Context(), `
		INSERT INTO gameskills (id, name, category, version, yaml_blob, builtin)
		VALUES (?,?,?,?,?,0)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, category=excluded.category,
			version=excluded.version, yaml_blob=excluded.yaml_blob
	`, id, gs.Name, gs.Category, gs.Version, string(data))
	if err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.auditLog(r, "gameskill.upload", "gameskill:"+id, nil)
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{"id": id, "name": gs.Name})
}

// isBuiltinRune reports whether a rune with this id exists and is built-in.
// Used to refuse any import/install path from overwriting (backdooring) a
// built-in rune that running servers may rely on.
func (s *Server) isBuiltinRune(ctx context.Context, id string) bool {
	var builtin int
	return s.db.QueryRowContext(ctx, "SELECT builtin FROM gameskills WHERE id=?", id).Scan(&builtin) == nil && builtin == 1
}

// handleImportEgg converts an uploaded Pterodactyl egg JSON into a gameskill,
// stores it as a (non-builtin) rune, and returns its id.
func (s *Server) handleImportEgg(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	gs, err := gameskill.ImportEgg(data)
	if err != nil {
		jsonError(w, "egg import: "+err.Error(), http.StatusBadRequest)
		return
	}
	if s.isBuiltinRune(r.Context(), gs.ID) {
		jsonError(w, "cannot overwrite a built-in rune; use a different id", http.StatusConflict)
		return
	}
	yamlBlob, err := gameskill.ToYAML(gs)
	if err != nil {
		jsonError(w, "serialize: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = s.db.ExecContext(r.Context(), `
		INSERT INTO gameskills (id, name, category, version, yaml_blob, builtin)
		VALUES (?,?,?,?,?,0)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, category=excluded.category,
			version=excluded.version, yaml_blob=excluded.yaml_blob
	`, gs.ID, gs.Name, gs.Category, gs.Version, string(yamlBlob))
	if err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "gameskill.import_egg", "gameskill:"+gs.ID, map[string]string{"name": gs.Name})
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{"id": gs.ID, "name": gs.Name})
}

// handleImportXML imports a gameskill expressed in XML.
func (s *Server) handleImportXML(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	gs, err := gameskill.ImportXML(data)
	if err != nil {
		jsonError(w, "xml import: "+err.Error(), http.StatusBadRequest)
		return
	}
	if s.isBuiltinRune(r.Context(), gs.ID) {
		jsonError(w, "cannot overwrite a built-in rune; use a different id", http.StatusConflict)
		return
	}
	yamlBlob, err := gameskill.ToYAML(gs)
	if err != nil {
		jsonError(w, "serialize: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = s.db.ExecContext(r.Context(), `
		INSERT INTO gameskills (id, name, category, version, yaml_blob, builtin)
		VALUES (?,?,?,?,?,0)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, category=excluded.category,
			version=excluded.version, yaml_blob=excluded.yaml_blob
	`, gs.ID, gs.Name, gs.Category, gs.Version, string(yamlBlob))
	if err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "gameskill.import_xml", "gameskill:"+gs.ID, map[string]string{"name": gs.Name})
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{"id": gs.ID, "name": gs.Name})
}

func (s *Server) handleDeleteGameskill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var builtin int
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT builtin FROM gameskills WHERE id=?", id).Scan(&builtin); err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if builtin == 1 {
		jsonError(w, "cannot delete built-in gameskill", http.StatusForbidden)
		return
	}

	if _, err := s.db.ExecContext(r.Context(),
		"DELETE FROM gameskills WHERE id=?", id); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}

	s.auditLog(r, "gameskill.delete", "gameskill:"+id, nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}

func (s *Server) auditLog(r *http.Request, action, resource string, detail interface{}) {
	claims := claimsFromContext(r.Context())
	userID := ""
	username := ""
	if claims != nil {
		userID = claims.UserID
		username = claims.Username
	}

	detailJSON := "null"
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			detailJSON = string(b)
		}
	}

	s.db.Exec(
		"INSERT INTO audit_log (id, user_id, username, action, resource, detail_json, ip, ts) VALUES (?,?,?,?,?,?,?,?)",
		uuid.New().String(), userID, username, action, resource, detailJSON,
		r.RemoteAddr, time.Now().UTC().Format(time.RFC3339),
	)
}
