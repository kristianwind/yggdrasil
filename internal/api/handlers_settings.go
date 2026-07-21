package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// getSetting reads an app_settings value (empty string if unset).
func (s *Server) getSetting(ctx context.Context, key string) string {
	var v string
	s.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key=?", key).Scan(&v)
	return v
}

// setSetting upserts an app_settings value.
func (s *Server) setSetting(ctx context.Context, key, value string) {
	s.db.ExecContext(ctx,
		"INSERT INTO app_settings (key, value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		key, value)
}

// handleGetNetworkSettings returns the public hostname (and the auto-detected
// fallback address so the UI can show what players would currently connect to).
func (s *Server) handleGetNetworkSettings(w http.ResponseWriter, r *http.Request) {
	host := s.getSetting(r.Context(), "public_hostname")
	jsonOK(w, map[string]any{
		"public_hostname":       host,
		"detected":              s.detectPublicAddr(),
		"internal":              localIP(),
		"effective":             firstNonEmpty(host, s.detectPublicAddr()),
		"upnp_enabled":          s.getSetting(r.Context(), "upnp_enabled") == "1",
		"battlemetrics_enabled": s.getSetting(r.Context(), "battlemetrics_token") != "",
	})
}

// handleSetNetworkSettings updates the public hostname.
func (s *Server) handleSetNetworkSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublicHostname     string  `json:"public_hostname"`
		UPnPEnabled        bool    `json:"upnp_enabled"`
		BattleMetricsToken *string `json:"battlemetrics_token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	host := strings.TrimSpace(req.PublicHostname)
	// Strip a scheme/port if the user pasted a URL — we only want the host.
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	host = strings.TrimSuffix(host, "/")
	s.setSetting(r.Context(), "public_hostname", host)
	s.setSetting(r.Context(), "upnp_enabled", boolStr(req.UPnPEnabled))
	// BattleMetrics token is optional; only overwrite when provided (so the UI can
	// omit it to keep the existing one). Encrypted at rest like other secrets.
	if req.BattleMetricsToken != nil {
		tok := strings.TrimSpace(*req.BattleMetricsToken)
		if tok == "" {
			s.setSetting(r.Context(), "battlemetrics_token", "")
		} else if enc, err := s.cipher.Encrypt(tok); err == nil {
			s.setSetting(r.Context(), "battlemetrics_token", enc)
		}
	}
	s.auditLog(r, "settings.network", "public_hostname", map[string]string{"host": host})
	jsonOK(w, map[string]any{
		"public_hostname": host,
		"effective":       firstNonEmpty(host, s.detectPublicAddr()),
		"upnp_enabled":    req.UPnPEnabled,
	})
}

// detectPublicAddr returns a best-effort connect address when no public hostname
// is configured: the external IP (cached) or, failing that, an empty string so
// the UI can fall back to the host's LAN IP shown elsewhere.
func (s *Server) detectPublicAddr() string {
	s.extIPMu.Lock()
	defer s.extIPMu.Unlock()
	if s.extIP != "" && time.Since(s.extIPAt) < time.Hour {
		return s.extIP
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.ipify.org", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return s.extIP // possibly stale, possibly empty
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	ip := strings.TrimSpace(string(b))
	if ip != "" {
		s.extIP = ip
		s.extIPAt = time.Now()
	}
	return s.extIP
}

// localIP returns the host's primary LAN IPv4 — the source address it would use
// to reach the network — for display in Settings so the admin can find the panel
// on their own network (and know which internal IP to point NPM/port-forwards at).
// The UDP "dial" sends no packets; it just resolves the routing decision.
// Best-effort: empty string if it can't be determined.
func localIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
