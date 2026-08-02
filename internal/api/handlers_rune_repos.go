package api

import (
	"errors"
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
	// Token is write-only: supplied on create/update, never returned. HasToken is
	// what a listing reports instead, so the UI can show that this repo carries its
	// own credential without the credential itself passing through a browser.
	Token    string `json:"token,omitempty"`
	HasToken bool   `json:"has_token,omitempty"`
}

// handleListRuneRepos returns the built-in catalog plus any user-added repos.
func (s *Server) handleListRuneRepos(w http.ResponseWriter, r *http.Request) {
	list := []runeRepoDTO{{
		ID: "default", Name: "Yggdrasil community catalog",
		Repo: defaultRuneRepo, Path: defaultRunePath, Ref: defaultRuneRef, Default: true,
	}}
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, name, repo, path, COALESCE(ref,'main'), COALESCE(token_enc,'') FROM rune_repos ORDER BY created_at")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var d runeRepoDTO
		var tokenEnc string
		if rows.Scan(&d.ID, &d.Name, &d.Repo, &d.Path, &d.Ref, &tokenEnc) == nil {
			d.HasToken = tokenEnc != ""
			list = append(list, d)
		}
	}
	jsonOK(w, list)
}

// storeRepoToken encrypts a token for storage. An empty token stores empty (the
// repo falls back to the panel-wide token), and encryption failing is reported
// rather than silently writing a plaintext credential into the database.
func (s *Server) storeRepoToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", nil
	}
	if s.cipher == nil {
		return "", errors.New("no encryption key available to store the token")
	}
	return s.cipher.Encrypt(token)
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
	tokenEnc, err := s.storeRepoToken(d.Token)
	if err != nil {
		jsonError(w, "could not store the token: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = s.db.ExecContext(r.Context(),
		"INSERT INTO rune_repos (id, name, repo, path, ref, token_enc) VALUES (?,?,?,?,?,?)",
		d.ID, d.Name, d.Repo, d.Path, d.Ref, tokenEnc)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "rune_repo.add", "rune_repo:"+d.ID, map[string]string{"repo": d.Repo})
	d.HasToken, d.Token = tokenEnc != "", ""
	jsonOK(w, d)
}

// handleUpdateRuneRepo edits a saved repository — in practice, attaching or
// clearing its own GitHub token after the fact. Token semantics match the rest of
// the panel: the mask (or an absent field) keeps what is stored, an empty string
// clears it, anything else replaces it.
func (s *Server) handleUpdateRuneRepo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Name  *string `json:"name"`
		Path  *string `json:"path"`
		Ref   *string `json:"ref"`
		Token *string `json:"token"`
	}
	if decodeJSON(r, &req) != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}
	var exists int
	s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM rune_repos WHERE id=?", id).Scan(&exists) //nolint:errcheck
	if exists == 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if req.Name != nil {
		if n := strings.TrimSpace(*req.Name); n != "" {
			s.db.ExecContext(r.Context(), "UPDATE rune_repos SET name=? WHERE id=?", n, id)
		}
	}
	if req.Path != nil {
		s.db.ExecContext(r.Context(), "UPDATE rune_repos SET path=? WHERE id=?",
			strings.Trim(strings.TrimSpace(*req.Path), "/"), id)
	}
	if req.Ref != nil {
		ref := strings.TrimSpace(*req.Ref)
		if ref == "" {
			ref = "main"
		}
		if !ghRefRe.MatchString(ref) {
			jsonError(w, "invalid ref", http.StatusBadRequest)
			return
		}
		s.db.ExecContext(r.Context(), "UPDATE rune_repos SET ref=? WHERE id=?", ref, id)
	}
	if req.Token != nil && *req.Token != secretMask {
		tokenEnc, err := s.storeRepoToken(*req.Token)
		if err != nil {
			jsonError(w, "could not store the token: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.db.ExecContext(r.Context(), "UPDATE rune_repos SET token_enc=? WHERE id=?", tokenEnc, id)
		// No cache flush needed: the listing cache is keyed by a fingerprint of the
		// EFFECTIVE token, so a changed credential simply cannot read the old
		// credential's cached listing.
		s.auditLog(r, "rune_repo.token", "rune_repo:"+id, map[string]any{"set": tokenEnc != ""})
	}
	s.auditLog(r, "rune_repo.update", "rune_repo:"+id, nil)
	jsonOK(w, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteRuneRepo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.db.ExecContext(r.Context(), "DELETE FROM rune_repos WHERE id=?", id)
	s.auditLog(r, "rune_repo.delete", "rune_repo:"+id, nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}
