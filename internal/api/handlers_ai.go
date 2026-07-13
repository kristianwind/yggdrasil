package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/llm"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// Advisory AI layer. An admin optionally wires up their own LLM (any provider);
// server features can then ask it for a plain-language read on machine-parsed
// data. First use: the admin-log "what happened while I was away" digest, built
// on the deterministic activity feed (#5). Strictly advisory + opt-in + the
// operator's own key/endpoint — no data leaves for anywhere they didn't set up,
// and nothing here acts on a server automatically.

type aiConfig struct {
	Provider string
	Model    string
	BaseURL  string
	APIKey   string
	Enabled  bool
}

// loadAIConfig reads the single ai_config row and decrypts the API key. Returns a
// zero config (Enabled=false) when unset.
func (s *Server) loadAIConfig(ctx context.Context) aiConfig {
	var c aiConfig
	var enc string
	var enabled int
	err := s.db.QueryRowContext(ctx,
		"SELECT provider, model, base_url, COALESCE(api_key_enc,''), enabled FROM ai_config WHERE id=1").
		Scan(&c.Provider, &c.Model, &c.BaseURL, &enc, &enabled)
	if err != nil {
		return aiConfig{}
	}
	c.Enabled = enabled == 1
	if enc != "" {
		if plain, derr := s.cipher.Decrypt(enc); derr == nil {
			c.APIKey = plain
		}
	}
	return c
}

// aiEnabled reports whether the advisory AI features are switched on (cheap check
// used by the per-server GET so the UI can show/hide the digest button).
func (s *Server) aiEnabled(ctx context.Context) bool {
	var enabled int
	s.db.QueryRowContext(ctx, "SELECT enabled FROM ai_config WHERE id=1").Scan(&enabled)
	return enabled == 1
}

type aiConfigView struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key"` // masked on GET; secretMask means "keep existing" on PUT
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"` // an API key is stored
}

func (s *Server) handleGetAIConfig(w http.ResponseWriter, r *http.Request) {
	c := s.loadAIConfig(r.Context())
	v := aiConfigView{Provider: c.Provider, Model: c.Model, BaseURL: c.BaseURL, Enabled: c.Enabled, Configured: c.APIKey != ""}
	if c.Provider == "" {
		v.Provider = "openai"
	}
	if v.Configured {
		v.APIKey = secretMask // never echo the key back
	}
	jsonOK(w, v)
}

func (s *Server) handleSetAIConfig(w http.ResponseWriter, r *http.Request) {
	var req aiConfigView
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	prev := s.loadAIConfig(r.Context())

	// Keep the existing key when the UI sends the mask sentinel (round-trip
	// without leaking); otherwise encrypt the new key.
	keyEnc := ""
	switch {
	case req.APIKey == secretMask || req.APIKey == "":
		// unchanged — re-encrypt the previous plaintext if we have one
		if prev.APIKey != "" {
			keyEnc, _ = s.cipher.Encrypt(prev.APIKey)
		}
	default:
		enc, err := s.cipher.Encrypt(req.APIKey)
		if err != nil {
			jsonError(w, "could not store key", http.StatusInternalServerError)
			return
		}
		keyEnc = enc
	}

	_, err := s.db.ExecContext(r.Context(), `
		INSERT INTO ai_config (id, provider, model, base_url, api_key_enc, enabled, updated_at)
		VALUES (1,?,?,?,?,?,datetime('now'))
		ON CONFLICT(id) DO UPDATE SET
			provider=excluded.provider, model=excluded.model, base_url=excluded.base_url,
			api_key_enc=excluded.api_key_enc, enabled=excluded.enabled, updated_at=excluded.updated_at`,
		strings.TrimSpace(req.Provider), strings.TrimSpace(req.Model), strings.TrimSpace(req.BaseURL),
		keyEnc, boolToInt(req.Enabled))
	if err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "ai.config", "ai", map[string]any{"provider": req.Provider, "model": req.Model, "enabled": req.Enabled})
	jsonOK(w, map[string]string{"status": "saved"})
}

// handleTestAIConfig sends a trivial prompt to verify the credentials/endpoint.
func (s *Server) handleTestAIConfig(w http.ResponseWriter, r *http.Request) {
	c := s.loadAIConfig(r.Context())
	if c.Model == "" || (c.APIKey == "" && !strings.EqualFold(c.Provider, "ollama")) {
		jsonError(w, "configure a model and API key first, then Save", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := llm.Complete(ctx, llm.Config{Provider: c.Provider, Model: c.Model, BaseURL: c.BaseURL, APIKey: c.APIKey},
		[]llm.Message{{Role: "user", Content: "Reply with exactly: OK"}}, 16)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]string{"status": "ok", "reply": out})
}

// handleAdminLogDigest asks the configured LLM for a plain-language read of the
// recent admin-log activity — "what happened while I was away", with anomaly
// flags. Advisory only; requires ServerView + an enabled AI config.
func (s *Server) handleAdminLogDigest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerView, srv.target()) {
		return
	}
	c := s.loadAIConfig(r.Context())
	if !c.Enabled {
		jsonError(w, "AI assistant is off — an admin can enable it in Settings", http.StatusBadRequest)
		return
	}
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil || rt.gs.AdminLog == nil {
		jsonError(w, "this server has no admin log to summarize", http.StatusBadRequest)
		return
	}
	file := s.newestAdminLog(srv.DataDir, rt.gs.AdminLog.Path)
	if file == "" {
		jsonOK(w, map[string]string{"summary": "No activity has been logged yet."})
		return
	}
	content, err := readTail(file, adminLogTailBytes)
	if err != nil {
		jsonError(w, "could not read the admin log", http.StatusBadGateway)
		return
	}
	events := parseAdminLog(content, rt.gs.AdminLog)
	if len(events) == 0 {
		jsonOK(w, map[string]string{"summary": "No notable activity in the recent log."})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, err := llm.Complete(ctx,
		llm.Config{Provider: c.Provider, Model: c.Model, BaseURL: c.BaseURL, APIKey: c.APIKey},
		buildDigestMessages(srv.Name, events), 700)
	if err != nil {
		jsonError(w, "AI request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.auditLog(r, "ai.digest", "server:"+id, nil)
	jsonOK(w, map[string]string{"summary": out})
}

// buildDigestMessages turns parsed admin-log events into a compact prompt asking
// for a short, advisory summary with anomaly flags. Pure + testable.
func buildDigestMessages(serverName string, events []adminLogEvent) []llm.Message {
	const maxEvents = 200
	if len(events) > maxEvents {
		events = events[:maxEvents]
	}
	var b strings.Builder
	for _, e := range events {
		t := e.Time
		if t == "" {
			t = "--:--:--"
		}
		player := e.Player
		if player == "" {
			player = "-"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\n", t, e.Type, player)
	}
	system := "You are a game-server admin assistant. You are given a list of recent activity-log " +
		"events (time, type, player) from a game server, newest first. Write a SHORT briefing (a few " +
		"sentences or a tight bullet list) of what happened while the admin was away. Call out anything " +
		"notable: unusually high joins/leaves, kill streaks, repeated deaths by the same player, or " +
		"players who joined and left immediately. Be concrete and reference player names and counts. " +
		"Do not invent events that aren't in the data. This is advisory only — do not suggest taking any " +
		"automated action."
	user := fmt.Sprintf("Server: %s\nRecent activity (newest first):\n%s", serverName, strings.TrimRight(b.String(), "\n"))
	return []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}
}
