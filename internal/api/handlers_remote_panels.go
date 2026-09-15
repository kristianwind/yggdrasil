package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Saved panel-to-panel connections.
//
// Moving a server between two panels needed the source's address and an admin
// token on it, typed in every time. That friction is not neutral: it is what
// makes people reuse one token everywhere, keep it in a note, and never rotate
// it. Saving the connection is the safer habit, provided the thing saved is not
// a skeleton key — which is what transfer-scoped tokens (auth.ScopeTransfer)
// are for.
//
// Rules this file keeps:
//
//   - The token is encrypted at rest and NEVER returned, not even to the admin
//     who saved it. The API answers has_token, the same shape as cf_api_token.
//   - Saving the token is optional and separable. An address and a name carry no
//     risk and remove half the friction on their own, so "forget the token" does
//     not mean "delete the connection".
//   - Every use is stamped, so a connection nothing needs any more is visible.

type remotePanelRow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	HasToken   bool   `json:"has_token"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	CreatedAt  string `json:"created_at"`
}

func (s *Server) handleListRemotePanels(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, name, url, COALESCE(token_enc,''), COALESCE(last_used_at,''), created_at
		 FROM remote_panels ORDER BY name`)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := []remotePanelRow{}
	for rows.Next() {
		var p remotePanelRow
		var enc string
		if rows.Scan(&p.ID, &p.Name, &p.URL, &enc, &p.LastUsedAt, &p.CreatedAt) != nil {
			continue
		}
		p.HasToken = enc != ""
		list = append(list, p)
	}
	jsonOK(w, list)
}

func (s *Server) handleSaveRemotePanel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string  `json:"id"` // empty = create
		Name  string  `json:"name"`
		URL   string  `json:"url"`
		Token *string `json:"token"` // nil = leave as-is; "" = forget it
	}
	if decodeJSON(r, &req) != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		jsonError(w, "a name is required — it is how you will recognise this panel in the list", http.StatusBadRequest)
		return
	}
	url, err := normalizeRemote(req.URL)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Encrypt before touching the database, so a cipher failure cannot leave a
	// row holding a plaintext token.
	var tokenEnc *string
	if req.Token != nil {
		if t := strings.TrimSpace(*req.Token); t == "" {
			empty := ""
			tokenEnc = &empty
		} else {
			enc, eerr := s.cipher.Encrypt(t)
			if eerr != nil {
				jsonError(w, "could not encrypt the token", http.StatusInternalServerError)
				return
			}
			tokenEnc = &enc
		}
	}

	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = uuid.New().String()
		enc := ""
		if tokenEnc != nil {
			enc = *tokenEnc
		}
		if _, err := s.db.ExecContext(r.Context(),
			"INSERT INTO remote_panels (id, name, url, token_enc) VALUES (?,?,?,?)", id, name, url, enc); err != nil {
			jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if _, err := s.db.ExecContext(r.Context(),
			"UPDATE remote_panels SET name=?, url=? WHERE id=?", name, url, id); err != nil {
			jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Only when the caller said something about the token. A form that omits
		// it — because it never had the value to show — must not wipe it.
		if tokenEnc != nil {
			s.db.ExecContext(r.Context(), "UPDATE remote_panels SET token_enc=? WHERE id=?", *tokenEnc, id)
		}
	}
	s.auditLog(r, "panel.remote_save", "remote:"+id,
		map[string]any{"name": name, "url": url, "token_set": tokenEnc != nil && *tokenEnc != ""})
	jsonOK(w, map[string]any{"id": id, "name": name, "url": url, "has_token": tokenEnc != nil && *tokenEnc != ""})
}

func (s *Server) handleDeleteRemotePanel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.db.ExecContext(r.Context(), "DELETE FROM remote_panels WHERE id=?", id); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "panel.remote_delete", "remote:"+id, nil)
	jsonOK(w, map[string]any{"deleted": true})
}

// resolveRemote turns either an explicit address+token or a saved connection's
// id into the pair the transfer code needs.
//
// Explicit values win when both are given, so a one-off pull to an address that
// is not saved keeps working exactly as before — this is an addition to the
// flow, not a replacement for it.
func (s *Server) resolveRemote(ctx context.Context, remoteID, rawURL, token string) (string, string, error) {
	remoteID = strings.TrimSpace(remoteID)
	if remoteID == "" {
		base, err := normalizeRemote(rawURL)
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(token) == "" {
			return "", "", fmt.Errorf("a token for the source panel is required")
		}
		return base, strings.TrimSpace(token), nil
	}

	var url, enc string
	if err := s.db.QueryRowContext(ctx,
		"SELECT url, COALESCE(token_enc,'') FROM remote_panels WHERE id=?", remoteID).Scan(&url, &enc); err != nil {
		return "", "", fmt.Errorf("no saved panel with that id")
	}
	// An explicitly supplied token overrides the saved one — that is how you use
	// a saved address whose token you never stored, without saving it now.
	if t := strings.TrimSpace(token); t != "" {
		return url, t, nil
	}
	if enc == "" {
		return "", "", fmt.Errorf("no token saved for this panel — paste one, or save it on the connection")
	}
	plain, err := s.cipher.Decrypt(enc)
	if err != nil {
		return "", "", fmt.Errorf("the saved token could not be decrypted — re-save it")
	}
	s.db.ExecContext(ctx, "UPDATE remote_panels SET last_used_at=datetime('now') WHERE id=?", remoteID)
	return url, plain, nil
}
