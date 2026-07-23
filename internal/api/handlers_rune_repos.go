package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Rune repositories the user has added on top of the built-in community catalog, so
// the Browse-runes UI can offer several sources and switch between them, and each
// installed rune is checked for updates against wherever it came from.

type runeRepoDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Repo    string `json:"repo"` // owner/repo
	Path    string `json:"path"`
	Ref     string `json:"ref"`
	Default bool   `json:"default,omitempty"` // the built-in catalog (not stored, not deletable)
}

// handleListRuneRepos returns the built-in catalog plus any user-added repos.
func (s *Server) handleListRuneRepos(w http.ResponseWriter, r *http.Request) {
	list := []runeRepoDTO{{
		ID: "default", Name: "Yggdrasil community catalog",
		Repo: defaultRuneRepo, Path: defaultRunePath, Ref: defaultRuneRef, Default: true,
	}}
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, name, repo, path, COALESCE(ref,'main') FROM rune_repos ORDER BY created_at")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var d runeRepoDTO
		if rows.Scan(&d.ID, &d.Name, &d.Repo, &d.Path, &d.Ref) == nil {
			list = append(list, d)
		}
	}
	jsonOK(w, list)
}

func (s *Server) handleCreateRuneRepo(w http.ResponseWriter, r *http.Request) {
	var d runeRepoDTO
	if decodeJSON(r, &d) != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}
	d.Repo = strings.TrimSpace(d.Repo)
	d.Path = strings.Trim(strings.TrimSpace(d.Path), "/")
	d.Ref = strings.TrimSpace(d.Ref)
	d.Name = strings.TrimSpace(d.Name)
	if !ghRepoRe.MatchString(d.Repo) {
		jsonError(w, "repo must be owner/name", http.StatusBadRequest)
		return
	}
	if d.Ref == "" {
		d.Ref = "main"
	}
	if !ghRefRe.MatchString(d.Ref) {
		jsonError(w, "invalid ref", http.StatusBadRequest)
		return
	}
	if d.Name == "" {
		d.Name = d.Repo
	}
	d.ID = uuid.New().String()
	_, err := s.db.ExecContext(r.Context(),
		"INSERT INTO rune_repos (id, name, repo, path, ref) VALUES (?,?,?,?,?)",
		d.ID, d.Name, d.Repo, d.Path, d.Ref)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "rune_repo.add", "rune_repo:"+d.ID, map[string]string{"repo": d.Repo})
	jsonOK(w, d)
}

func (s *Server) handleDeleteRuneRepo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.db.ExecContext(r.Context(), "DELETE FROM rune_repos WHERE id=?", id)
	s.auditLog(r, "rune_repo.delete", "rune_repo:"+id, nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}
