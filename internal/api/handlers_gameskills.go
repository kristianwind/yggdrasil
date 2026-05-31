package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

func (s *Server) handleListGameskills(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, name, category, version, builtin FROM gameskills ORDER BY name")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type item struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Category string `json:"category"`
		Version  int    `json:"version"`
		Builtin  bool   `json:"builtin"`
	}
	var list []item
	for rows.Next() {
		var it item
		var builtin int
		if err := rows.Scan(&it.ID, &it.Name, &it.Category, &it.Version, &builtin); err != nil {
			continue
		}
		it.Builtin = builtin == 1
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
