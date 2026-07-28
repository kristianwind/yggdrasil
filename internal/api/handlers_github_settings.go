package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GitHub token settings — lets the rune browser read PRIVATE repositories (and
// lifts the unauthenticated 60-requests/hour API limit). Stored encrypted at rest
// like the other secrets; the value is never returned to the client, only whether
// one is configured. A classic PAT needs the `repo` scope; a fine-grained token
// needs Repository permissions → Contents: Read-only on the repos in question.

// handleGetGithubSettings reports whether a token is stored (never the token).
func (s *Server) handleGetGithubSettings(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{"configured": s.getSetting(r.Context(), "github_token") != ""})
}

// handleSetGithubSettings stores or clears the token. An empty string clears it.
func (s *Server) handleSetGithubSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	tok := strings.TrimSpace(req.Token)
	if tok == "" {
		s.setSetting(r.Context(), "github_token", "")
		s.invalidateGithubRuneCache()
		s.auditLog(r, "settings.github_token", "github_token", map[string]any{"action": "cleared"})
		jsonOK(w, map[string]any{"configured": false})
		return
	}
	enc, err := s.cipher.Encrypt(tok)
	if err != nil {
		jsonError(w, "could not encrypt the token", http.StatusInternalServerError)
		return
	}
	s.setSetting(r.Context(), "github_token", enc)
	// A new credential can see different repositories, so don't serve listings the
	// old one produced.
	s.invalidateGithubRuneCache()
	s.auditLog(r, "settings.github_token", "github_token", map[string]any{"action": "saved"})
	jsonOK(w, map[string]any{"configured": true})
}

// handleTestGithubToken verifies the stored token against GitHub and reports which
// account it belongs to, so an admin can tell a working token from a typo without
// guessing from a failed rune listing.
func (s *Server) handleTestGithubToken(w http.ResponseWriter, r *http.Request) {
	token := s.githubToken(r.Context())
	if token == "" {
		jsonError(w, "no GitHub token is configured", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	login, err := githubWhoami(ctx, token)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]any{"ok": true, "login": login})
}

// githubWhoami returns the login the token authenticates as.
func githubWhoami(ctx context.Context, token string) (string, error) {
	resp, err := ghHTTP(ctx, "GET", "https://api.github.com/user", "application/vnd.github+json", token)
	if err != nil {
		return "", fmt.Errorf("github unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("github rejected the token (401) — it may be expired or mistyped")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github returned %d", resp.StatusCode)
	}
	var out struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("parse github response: %w", err)
	}
	if out.Login == "" {
		return "", fmt.Errorf("github returned no account for this token")
	}
	return out.Login, nil
}

// invalidateGithubRuneCache drops every cached repo listing.
func (s *Server) invalidateGithubRuneCache() {
	ghRunesMu.Lock()
	ghRunesCache = map[string]ghRunesEntry{}
	ghRunesMu.Unlock()
}
