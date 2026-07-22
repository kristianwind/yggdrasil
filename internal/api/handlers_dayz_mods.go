package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/llm"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// The DayZ mod manager: add by Workshop ID, remove, drag-reorder (load order
// matters), search the Workshop (needs a Steam Web API key), and a Kvasir helper
// that flags missing dependencies and a sane load order. Mods live in the MODS env
// var as a ';'-ordered id list; dayzModIDs parses it, dayzWriteMods rewrites it.

var wsIDRe = func(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// dayzServerMods reads a DayZ server's ordered MODS ids (error if not a DayZ rune).
func (s *Server) dayzServerMods(ctx context.Context, id string) ([]string, bool) {
	var gameskillID, envJSON string
	if err := s.db.QueryRowContext(ctx,
		"SELECT gameskill_id, env_json FROM servers WHERE id=?", id).Scan(&gameskillID, &envJSON); err != nil || gameskillID != "dayz" {
		return nil, false
	}
	env := map[string]string{}
	json.Unmarshal([]byte(envJSON), &env) //nolint:errcheck
	return dayzModIDs(env), true
}

// dayzWriteMods rewrites MODS to the given ordered ids, leaving every other
// (possibly-encrypted) env value untouched.
func (s *Server) dayzWriteMods(ctx context.Context, id string, ids []string) error {
	var envJSON string
	if err := s.db.QueryRowContext(ctx, "SELECT env_json FROM servers WHERE id=?", id).Scan(&envJSON); err != nil {
		return err
	}
	env := map[string]string{}
	json.Unmarshal([]byte(envJSON), &env) //nolint:errcheck
	env["MODS"] = strings.Join(ids, ";")
	b, _ := json.Marshal(env)
	_, err := s.db.ExecContext(ctx, "UPDATE servers SET env_json=? WHERE id=?", string(b), id)
	return err
}

// handleDayzAddMod appends a Workshop id to MODS. Validates the id against the
// Steam Workshop and refuses one Steam definitively reports as removed/not found.
// The mod downloads on the next Update/Reinstall.
func (s *Server) handleDayzAddMod(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerControl, s.serverTarget(r.Context(), id)) {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if decodeJSON(r, &req) != nil || !wsIDRe(strings.TrimSpace(req.ID)) {
		jsonError(w, "a numeric Workshop id is required", http.StatusBadRequest)
		return
	}
	modID := strings.TrimSpace(req.ID)
	ids, ok := s.dayzServerMods(r.Context(), id)
	if !ok {
		jsonError(w, "not a DayZ server", http.StatusBadRequest)
		return
	}
	for _, m := range ids {
		if m == modID {
			jsonError(w, "that mod is already in the list", http.StatusConflict)
			return
		}
	}
	ws := dayzWorkshopLookup(r.Context(), []string{modID})
	it := ws[modID]
	if it.Known && it.Removed {
		jsonError(w, "Steam has no available Workshop item with that id (removed, private, or wrong id)", http.StatusBadRequest)
		return
	}
	if err := s.dayzWriteMods(r.Context(), id, append(ids, modID)); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	name := it.Title
	if name == "" {
		name = modID
	}
	s.auditLog(r, "dayz.mod.add", "server:"+id, map[string]string{"mod": modID})
	jsonOK(w, map[string]any{"added": modID, "name": name, "count": len(ids) + 1})
}

// handleDayzRemoveMod drops a Workshop id from MODS. The @<id> folder on disk is
// left in place (a later reinstall prunes it); startup just stops loading it.
func (s *Server) handleDayzRemoveMod(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerControl, s.serverTarget(r.Context(), id)) {
		return
	}
	modID := strings.TrimSpace(r.URL.Query().Get("id"))
	if modID == "" {
		jsonError(w, "id required", http.StatusBadRequest)
		return
	}
	ids, ok := s.dayzServerMods(r.Context(), id)
	if !ok {
		jsonError(w, "not a DayZ server", http.StatusBadRequest)
		return
	}
	out := make([]string, 0, len(ids))
	for _, m := range ids {
		if m != modID {
			out = append(out, m)
		}
	}
	if len(out) == len(ids) {
		jsonError(w, "that mod is not in the list", http.StatusNotFound)
		return
	}
	if err := s.dayzWriteMods(r.Context(), id, out); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "dayz.mod.remove", "server:"+id, map[string]string{"mod": modID})
	jsonOK(w, map[string]any{"removed": modID, "count": len(out)})
}

// handleDayzReorderMods rewrites the load order. The body must be a permutation of
// the current ids (no adds/drops) — reordering is a separate action from add/remove
// so a stale client can't silently wipe the list. Takes effect on the next restart.
func (s *Server) handleDayzReorderMods(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerControl, s.serverTarget(r.Context(), id)) {
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if decodeJSON(r, &req) != nil {
		jsonError(w, "ids required", http.StatusBadRequest)
		return
	}
	ids, ok := s.dayzServerMods(r.Context(), id)
	if !ok {
		jsonError(w, "not a DayZ server", http.StatusBadRequest)
		return
	}
	if len(req.IDs) != len(ids) {
		jsonError(w, "reorder must be the same set of mods, just reordered", http.StatusBadRequest)
		return
	}
	have := map[string]bool{}
	for _, m := range ids {
		have[m] = true
	}
	for _, m := range req.IDs {
		if !have[m] {
			jsonError(w, "reorder must be the same set of mods, just reordered", http.StatusBadRequest)
			return
		}
		delete(have, m) // catches duplicates too
	}
	if err := s.dayzWriteMods(r.Context(), id, req.IDs); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "dayz.mod.reorder", "server:"+id, nil)
	jsonOK(w, map[string]any{"order": req.IDs})
}

type dayzSearchResult struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Preview string `json:"preview"`
	URL     string `json:"url"`
}

// handleDayzSearchMods searches the DayZ Steam Workshop (app 221100). Needs a Steam
// Web API key (Settings → Integrations) — the keyless details endpoint we use for
// validation can't search. Returns needs_key=true when one isn't configured so the
// UI can point the admin to add it (paste-by-id still works without a key).
func (s *Server) handleDayzSearchMods(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerView, s.serverTarget(r.Context(), id)) {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		jsonOK(w, map[string]any{"results": []dayzSearchResult{}})
		return
	}
	key := ""
	if enc := s.getSetting(r.Context(), "steam_web_api_key"); enc != "" {
		key, _ = s.cipher.Decrypt(enc)
	}
	if key == "" {
		jsonOK(w, map[string]any{"results": []dayzSearchResult{}, "needs_key": true})
		return
	}
	form := url.Values{}
	form.Set("key", key)
	form.Set("appid", "221100")
	form.Set("search_text", q)
	form.Set("numperpage", "24")
	form.Set("query_type", "12") // ranked by text search
	form.Set("return_previews", "true")
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET",
		"https://api.steampowered.com/IPublishedFileService/QueryFiles/v1/?"+form.Encode(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		jsonError(w, "workshop search failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		jsonError(w, "Steam rejected the Web API key", http.StatusBadGateway)
		return
	}
	var parsed struct {
		Response struct {
			Details []struct {
				ID      string `json:"publishedfileid"`
				Title   string `json:"title"`
				Preview string `json:"preview_url"`
			} `json:"publishedfiledetails"`
		} `json:"response"`
	}
	if json.NewDecoder(resp.Body).Decode(&parsed) != nil {
		jsonError(w, "workshop search parse error", http.StatusBadGateway)
		return
	}
	out := make([]dayzSearchResult, 0, len(parsed.Response.Details))
	for _, d := range parsed.Response.Details {
		if d.ID == "" || d.Title == "" {
			continue
		}
		out = append(out, dayzSearchResult{ID: d.ID, Title: d.Title, Preview: d.Preview, URL: dayzWorkshopURL(d.ID)})
	}
	jsonOK(w, map[string]any{"results": out})
}

// handleDayzSuggestMods asks Kvasir to review the configured mod list and flag
// likely-missing dependencies (frameworks like CF / Dabs Framework) and a sane load
// order. Advisory only — it never edits the list. Needs the AI configured.
func (s *Server) handleDayzSuggestMods(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerView, s.serverTarget(r.Context(), id)) {
		return
	}
	ids, ok := s.dayzServerMods(r.Context(), id)
	if !ok {
		jsonError(w, "not a DayZ server", http.StatusBadRequest)
		return
	}
	if len(ids) == 0 {
		jsonOK(w, map[string]any{"dependencies": []any{}, "order_note": "No mods configured yet.", "recommended_order": []string{}})
		return
	}
	cfg := s.loadAIConfig(r.Context())
	if !cfg.Enabled || cfg.APIKey == "" {
		jsonError(w, "configure an AI provider in Settings → Kvasir to use suggestions", http.StatusBadRequest)
		return
	}
	// Resolve names so the model reasons about mods, not bare ids.
	ws := dayzWorkshopLookup(r.Context(), ids)
	var list strings.Builder
	for i, m := range ids {
		name := ws[m].Title
		if name == "" {
			name = "(unknown)"
		}
		fmt.Fprintf(&list, "%d. %s — %s\n", i+1, m, name)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	out, err := llm.Complete(ctx,
		llm.Config{Provider: cfg.Provider, Model: cfg.Model, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey},
		dayzModSuggestMessages(list.String(), ids), 900)
	if err != nil {
		jsonError(w, "the AI request failed or timed out", http.StatusBadGateway)
		return
	}
	sug := parseDayzModSuggestion(out, ids)
	jsonOK(w, sug)
}

// dayzModSuggestMessages is the dependency/order prompt. Pure + testable.
func dayzModSuggestMessages(list string, ids []string) []llm.Message {
	system := "You are Kvasir, a DayZ server assistant. You are given the Workshop mods a server loads, " +
		"in their current load order. Do two things: (1) flag well-known DEPENDENCIES these mods need that " +
		"appear to be MISSING from the list — DayZ frameworks such as CF (Community Framework), Dabs Framework, " +
		"Community-Online-Tools, VPPAdminTools, and any mod-specific dependency you recognise; do not invent ids. " +
		"(2) recommend a correct LOAD ORDER: frameworks and dependencies must load BEFORE the mods that use them. " +
		"Respond with ONLY a JSON object, no prose or fences:\n" +
		`{"dependencies":[{"name":"<framework>","reason":"<which listed mod needs it>"}],` +
		`"order_note":"<one sentence on the ordering>","recommended_order":["<id>","<id>"]}` + "\n" +
		"recommended_order must be a permutation of exactly the ids given — never add or drop ids. If the order is " +
		"already fine, return the same order. If nothing is missing, return an empty dependencies array."
	user := "Mods (load order):\n" + list + "\nThe ids, for recommended_order: " + strings.Join(ids, ", ")
	return []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}
}

type dayzModSuggestion struct {
	Dependencies []struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
	} `json:"dependencies"`
	OrderNote        string   `json:"order_note"`
	RecommendedOrder []string `json:"recommended_order"`
}

// parseDayzModSuggestion tolerantly extracts the JSON and, crucially, discards a
// recommended_order that isn't an exact permutation of the real ids — the model
// must never be able to inject or drop a mod through a suggestion.
func parseDayzModSuggestion(out string, ids []string) dayzModSuggestion {
	if i := strings.Index(out, "{"); i >= 0 {
		if j := strings.LastIndex(out, "}"); j > i {
			out = out[i : j+1]
		}
	}
	var d dayzModSuggestion
	json.Unmarshal([]byte(out), &d) //nolint:errcheck
	if !sameIDSet(d.RecommendedOrder, ids) {
		d.RecommendedOrder = nil // don't surface an order that adds/drops mods
	}
	return d
}

// handleGetSteamKey reports only whether a Steam Web API key is stored, never the
// value (write-only, like other credentials).
func (s *Server) handleGetSteamKey(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{"configured": s.getSetting(r.Context(), "steam_web_api_key") != ""})
}

// handleSetSteamKey stores (encrypted) or clears the Steam Web API key used for
// Workshop search. An empty value clears it.
func (s *Server) handleSetSteamKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if decodeJSON(r, &req) != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		s.setSetting(r.Context(), "steam_web_api_key", "")
	} else if enc, err := s.cipher.Encrypt(key); err == nil {
		s.setSetting(r.Context(), "steam_web_api_key", enc)
	} else {
		jsonError(w, "encrypt error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "settings.steam_web_api_key", "steam", map[string]any{"configured": key != ""})
	jsonOK(w, map[string]any{"configured": key != ""})
}

func sameIDSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range b {
		seen[x]++
	}
	for _, x := range a {
		if seen[x] == 0 {
			return false
		}
		seen[x]--
	}
	return true
}
