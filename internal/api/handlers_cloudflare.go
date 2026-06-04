package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/kristianwind/yggdrasil/internal/cloudflare"
)

// joinSubdomain resolves a label (or full custom domain) against a base domain.
// A value containing a dot is treated as a full domain; otherwise it's joined to
// base. Returns "" when not resolvable. Shared shape with npmFullDomain.
func joinSubdomain(sub, base string) string {
	sub = normalizeSubdomain(sub)
	if sub == "" {
		return ""
	}
	if strings.Contains(sub, ".") {
		return sub
	}
	base = normalizeSubdomain(base)
	if base == "" {
		return ""
	}
	return sub + "." + base
}

// cfClient builds a Cloudflare client from stored settings, or returns nil if
// the integration isn't configured/enabled. Resolves & caches the zone id from
// the base domain when not set explicitly.
func (s *Server) cfClient(ctx context.Context) (*cloudflare.Client, error) {
	if s.getSetting(ctx, "cf_enabled") != "1" {
		return nil, nil
	}
	encToken := s.getSetting(ctx, "cf_api_token")
	account := s.getSetting(ctx, "cf_account_id")
	tunnel := s.getSetting(ctx, "cf_tunnel_id")
	if encToken == "" || account == "" || tunnel == "" {
		return nil, nil
	}
	token, err := s.cipher.Decrypt(encToken)
	if err != nil {
		return nil, fmt.Errorf("decrypt Cloudflare token: %w", err)
	}
	zone := s.getSetting(ctx, "cf_zone_id")
	c := cloudflare.New(token, account, zone, tunnel)
	if zone == "" {
		base := s.getSetting(ctx, "cf_base_domain")
		if base == "" {
			return nil, fmt.Errorf("cloudflare: zone id or base domain required")
		}
		resolved, err := c.ResolveZoneID(base)
		if err != nil {
			return nil, err
		}
		c.SetZoneID(resolved)
		s.setSetting(ctx, "cf_zone_id", resolved) // cache for next time
	}
	return c, nil
}

// cfFullDomain resolves a server's subdomain to a full domain via cf_base_domain.
func (s *Server) cfFullDomain(ctx context.Context, sub string) string {
	return joinSubdomain(sub, s.getSetting(ctx, "cf_base_domain"))
}

// cfAddServer creates/refreshes a tunnel ingress rule + proxied CNAME routing the
// server's subdomain to <internal_host>:<web port> (best-effort).
func (s *Server) cfAddServer(serverID, serverName string) {
	defer recoverLog("cfAddServer")
	ctx := context.Background()

	var sub string
	s.db.QueryRowContext(ctx, "SELECT COALESCE(subdomain,'') FROM servers WHERE id=?", serverID).Scan(&sub)
	if normalizeSubdomain(sub) == "" {
		return
	}
	domain := s.cfFullDomain(ctx, sub)
	if domain == "" {
		return
	}
	port := s.serverWebPort(ctx, serverID)
	if port == 0 {
		return // UDP-only / no web port → nothing to proxy
	}
	c, err := s.cfClient(ctx)
	if err != nil || c == nil {
		return
	}
	internalHost := firstNonEmpty(s.getSetting(ctx, "cf_internal_host"), localLANIP())
	if internalHost == "" {
		return
	}

	// If the subdomain changed, the old hostname is still recorded — tear it down
	// first so we don't leave an orphan ingress rule / DNS record behind.
	var old string
	s.db.QueryRowContext(ctx, "SELECT COALESCE(cf_hostname,'') FROM servers WHERE id=?", serverID).Scan(&old)
	if old != "" && !strings.EqualFold(old, domain) {
		_ = c.RemoveHostname(old)
		_ = c.RemoveDNS(old)
	}

	service := fmt.Sprintf("http://%s:%d", internalHost, port)
	if err := c.UpsertHostname(domain, service); err != nil {
		return // ingress is the critical part; don't record a half-applied state
	}
	_ = c.EnsureDNS(domain) // DNS failure is non-fatal (record may be managed manually)
	s.db.ExecContext(ctx, "UPDATE servers SET cf_hostname=? WHERE id=?", domain, serverID)
}

// cfRemoveServer deletes the server's tunnel ingress rule + CNAME (best-effort)
// and clears the recorded hostname.
func (s *Server) cfRemoveServer(serverID string) {
	defer recoverLog("cfRemoveServer")
	ctx := context.Background()
	var host string
	s.db.QueryRowContext(ctx, "SELECT COALESCE(cf_hostname,'') FROM servers WHERE id=?", serverID).Scan(&host)
	if host == "" {
		return
	}
	c, err := s.cfClient(ctx)
	if err != nil || c == nil {
		return
	}
	_ = c.RemoveHostname(host)
	_ = c.RemoveDNS(host)
	s.db.ExecContext(ctx, "UPDATE servers SET cf_hostname='' WHERE id=?", serverID)
}

// --- Settings endpoints ---

func (s *Server) handleGetCloudflareSettings(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"account_id":    s.getSetting(r.Context(), "cf_account_id"),
		"zone_id":       s.getSetting(r.Context(), "cf_zone_id"),
		"tunnel_id":     s.getSetting(r.Context(), "cf_tunnel_id"),
		"base_domain":   s.getSetting(r.Context(), "cf_base_domain"),
		"internal_host": s.getSetting(r.Context(), "cf_internal_host"),
		"enabled":       s.getSetting(r.Context(), "cf_enabled") == "1",
		"configured":    s.getSetting(r.Context(), "cf_api_token") != "",
	})
}

func (s *Server) handleSetCloudflareSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token        string `json:"token"` // blank = keep existing
		AccountID    string `json:"account_id"`
		ZoneID       string `json:"zone_id"`
		TunnelID     string `json:"tunnel_id"`
		BaseDomain   string `json:"base_domain"`
		InternalHost string `json:"internal_host"`
		Enabled      bool   `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	s.setSetting(r.Context(), "cf_account_id", strings.TrimSpace(req.AccountID))
	s.setSetting(r.Context(), "cf_zone_id", strings.TrimSpace(req.ZoneID))
	s.setSetting(r.Context(), "cf_tunnel_id", strings.TrimSpace(req.TunnelID))
	s.setSetting(r.Context(), "cf_base_domain", normalizeSubdomain(req.BaseDomain))
	s.setSetting(r.Context(), "cf_internal_host", strings.TrimSpace(req.InternalHost))
	s.setSetting(r.Context(), "cf_enabled", boolStr(req.Enabled))
	if req.Token != "" {
		if enc, err := s.cipher.Encrypt(req.Token); err == nil {
			s.setSetting(r.Context(), "cf_api_token", enc)
		}
	}
	s.auditLog(r, "settings.cloudflare", "cloudflare", map[string]any{"enabled": req.Enabled})
	s.handleGetCloudflareSettings(w, r)
}

// handleTestCloudflare verifies the token, resolves the zone from the base domain,
// and confirms the tunnel config is reachable.
func (s *Server) handleTestCloudflare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token      string `json:"token"`
		AccountID  string `json:"account_id"`
		ZoneID     string `json:"zone_id"`
		TunnelID   string `json:"tunnel_id"`
		BaseDomain string `json:"base_domain"`
	}
	decodeJSON(r, &req)
	token := req.Token
	if token == "" {
		if enc := s.getSetting(r.Context(), "cf_api_token"); enc != "" {
			token, _ = s.cipher.Decrypt(enc)
		}
	}
	account := firstNonEmpty(strings.TrimSpace(req.AccountID), s.getSetting(r.Context(), "cf_account_id"))
	tunnel := firstNonEmpty(strings.TrimSpace(req.TunnelID), s.getSetting(r.Context(), "cf_tunnel_id"))
	zone := firstNonEmpty(strings.TrimSpace(req.ZoneID), s.getSetting(r.Context(), "cf_zone_id"))
	base := firstNonEmpty(normalizeSubdomain(req.BaseDomain), s.getSetting(r.Context(), "cf_base_domain"))
	if token == "" || account == "" || tunnel == "" {
		jsonError(w, "API token, account ID and tunnel ID are required", http.StatusBadRequest)
		return
	}
	c := cloudflare.New(token, account, zone, tunnel)
	if err := c.Verify(); err != nil {
		jsonError(w, "token check failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if zone == "" && base != "" {
		resolved, err := c.ResolveZoneID(base)
		if err != nil {
			jsonError(w, "token ok, but resolving the zone for "+base+" failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		zone = resolved
		s.setSetting(r.Context(), "cf_zone_id", resolved)
	}
	jsonOK(w, map[string]any{"ok": true, "zone_id": zone})
}
