package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// BattleMetrics gives an at-a-glance online status for a server from the public
// BattleMetrics API. Each server optionally stores a BattleMetrics server id; an
// optional account token (Settings → Network) raises the rate limit but is not
// required for public server data.

type bmEntry struct {
	at   time.Time
	data map[string]any
}

var (
	bmMu    sync.Mutex
	bmStore = map[string]bmEntry{}
)

// handleServerBattleMetrics returns a compact online-status summary for a server
// from BattleMetrics, if the server has a BattleMetrics id configured.
func (s *Server) handleServerBattleMetrics(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerView, s.serverTarget(r.Context(), id)) {
		return
	}
	var bmID string
	s.db.QueryRowContext(r.Context(), "SELECT COALESCE(bm_server_id,'') FROM servers WHERE id=?", id).Scan(&bmID)
	if bmID == "" {
		jsonOK(w, map[string]any{"configured": false})
		return
	}
	data, err := s.battlemetricsLookup(r.Context(), bmID)
	if err != nil {
		jsonOK(w, map[string]any{"configured": true, "online": false, "error": err.Error()})
		return
	}
	jsonOK(w, data)
}

// battlemetricsLookup fetches + parses a BattleMetrics server, with a short cache
// to stay well under the public rate limit when the UI polls.
func (s *Server) battlemetricsLookup(ctx context.Context, bmID string) (map[string]any, error) {
	bmMu.Lock()
	if e, ok := bmStore[bmID]; ok && time.Since(e.at) < 30*time.Second {
		bmMu.Unlock()
		return e.data, nil
	}
	bmMu.Unlock()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.battlemetrics.com/servers/"+url.PathEscape(bmID), nil)
	if tok := s.getSetting(ctx, "battlemetrics_token"); tok != "" {
		if dec, err := s.cipher.Decrypt(tok); err == nil {
			tok = dec // encrypted at rest; fall back to raw for any legacy plaintext value
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("server id not found on BattleMetrics")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("BattleMetrics HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Data struct {
			Attributes struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Players    int    `json:"players"`
				MaxPlayers int    `json:"maxPlayers"`
				Rank       int    `json:"rank"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	a := raw.Data.Attributes
	out := map[string]any{
		"configured":  true,
		"online":      a.Status == "online",
		"status":      a.Status,
		"name":        a.Name,
		"players":     a.Players,
		"max_players": a.MaxPlayers,
		"rank":        a.Rank,
		"url":         "https://www.battlemetrics.com/servers/" + bmID,
	}
	bmMu.Lock()
	bmStore[bmID] = bmEntry{at: time.Now(), data: out}
	bmMu.Unlock()
	return out, nil
}
