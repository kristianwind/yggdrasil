package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Public status page. A read-only, unauthenticated up/down board so players can
// check whether a server is online without a panel account — the thing people
// most often want to know. Privacy-first: it exposes NOTHING until an admin opts
// in per server AND flips the master switch, and even then only a server's
// display name, game, up/down state and current player count — never IDs, ports,
// addresses, env or any operational detail. When the master switch is off both
// the page and the API 404, so the panel's existence isn't advertised.
//
// The JSON is cached briefly because the endpoint is unauthenticated (a cheap
// abuse guard); player counts come from the metrics the panel already samples, so
// rendering the page never touches a game server.

const (
	statusCacheTTL     = 15 * time.Second
	statusPlayerMaxAge = "-15 minutes" // ignore metric samples older than this for the live count
)

func (s *Server) statusPageEnabled(ctx context.Context) bool {
	return s.getSetting(ctx, "status_page_enabled") == "1"
}

func (s *Server) statusPageTitle(ctx context.Context) string {
	return firstNonEmpty(s.getSetting(ctx, "status_page_title"), "Server Status")
}

type publicServerStatus struct {
	Name    string `json:"name"`
	Game    string `json:"game,omitempty"`
	Status  string `json:"status"` // online | starting | offline
	Players *int   `json:"players,omitempty"`
}

type publicStatusResponse struct {
	Title     string               `json:"title"`
	Servers   []publicServerStatus `json:"servers"`
	UpdatedAt string               `json:"updated_at"`
}

// buildPublicStatus assembles the current public board from the DB (no game-server
// I/O — player counts come from the latest sampled metric).
func (s *Server) buildPublicStatus(ctx context.Context) publicStatusResponse {
	resp := publicStatusResponse{
		Title:     s.statusPageTitle(ctx),
		Servers:   []publicServerStatus{},
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.name, COALESCE(g.name,''), s.status, s.installed
		FROM servers s LEFT JOIN gameskills g ON g.id = s.gameskill_id
		WHERE COALESCE(s.status_public,0)=1
		ORDER BY s.name`)
	if err != nil {
		return resp
	}
	type row struct {
		id, name, game, status string
		installed              int
	}
	var list []row
	for rows.Next() {
		var x row
		if rows.Scan(&x.id, &x.name, &x.game, &x.status, &x.installed) == nil {
			list = append(list, x)
		}
	}
	rows.Close()

	for _, x := range list {
		ps := publicServerStatus{Name: x.name, Game: x.game, Status: "offline"}
		switch {
		case x.installed == 1 && x.status == "running":
			ps.Status = "online"
			if n := s.latestSampledPlayers(ctx, x.id); n >= 0 {
				ps.Players = &n
			}
		case x.installed == 1 && x.status == "starting":
			ps.Status = "starting"
		}
		resp.Servers = append(resp.Servers, ps)
	}
	return resp
}

// latestSampledPlayers returns the most recent sampled player count for a server
// (within statusPlayerMaxAge), or -1 when there's no fresh sample or the game has
// no query. Reuses the metrics the sampler already collects — no live query here.
func (s *Server) latestSampledPlayers(ctx context.Context, serverID string) int {
	var players int
	err := s.db.QueryRowContext(ctx, `
		SELECT players FROM metrics
		WHERE server_id=? AND ts >= datetime('now', ?)
		ORDER BY ts DESC LIMIT 1`, serverID, statusPlayerMaxAge).Scan(&players)
	if err != nil {
		return -1
	}
	return players
}

// handlePublicStatus serves the cached public status JSON (404 when disabled).
func (s *Server) handlePublicStatus(w http.ResponseWriter, r *http.Request) {
	if !s.statusPageEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	s.statusMu.Lock()
	if s.statusCache == nil || time.Since(s.statusCacheAt) > statusCacheTTL {
		b, _ := json.Marshal(s.buildPublicStatus(r.Context()))
		s.statusCache = b
		s.statusCacheAt = time.Now()
	}
	body := s.statusCache
	s.statusMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=15")
	w.Write(body)
}

// invalidateStatusCache drops the cached board so a config change (enable/title/
// per-server opt-in) shows up immediately instead of after the TTL.
func (s *Server) invalidateStatusCache() {
	s.statusMu.Lock()
	s.statusCache = nil
	s.statusMu.Unlock()
}

// handleStatusPage serves the public status HTML (404 when disabled). Its script
// is loaded from /status.js because the panel's strict CSP forbids inline scripts.
func (s *Server) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	if !s.statusPageEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(statusPageHTML))
}

// handleStatusPageJS serves the status page's script as a same-origin asset.
func (s *Server) handleStatusPageJS(w http.ResponseWriter, r *http.Request) {
	if !s.statusPageEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write([]byte(statusPageJS))
}

// handleGetStatusSettings returns the status-page config (admin).
func (s *Server) handleGetStatusSettings(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"enabled": s.statusPageEnabled(r.Context()),
		"title":   s.statusPageTitle(r.Context()),
	})
}

// handleSetStatusSettings updates the status-page config (admin).
func (s *Server) handleSetStatusSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool   `json:"enabled"`
		Title   *string `json:"title"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Enabled != nil {
		s.setSetting(r.Context(), "status_page_enabled", boolStr(*req.Enabled))
	}
	if req.Title != nil {
		s.setSetting(r.Context(), "status_page_title", *req.Title)
	}
	s.invalidateStatusCache()
	s.auditLog(r, "settings.status_page", "status_page", map[string]any{"enabled": req.Enabled != nil && *req.Enabled})
	s.handleGetStatusSettings(w, r)
}
