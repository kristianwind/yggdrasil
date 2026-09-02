package api

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// cfAddServer provisions every hostname the server claims: a tunnel ingress rule
// plus a proxied CNAME for each. Best-effort throughout — a route that cannot be
// applied is skipped rather than aborting the others, because one bad hostname
// should not cost a server its working ones.
func (s *Server) cfAddServer(serverID, serverName string) {
	defer recoverLog("cfAddServer")
	ctx := context.Background()

	routes := s.serverRoutes(ctx, serverID)
	if len(routes) == 0 {
		return
	}
	c, err := s.cfClient(ctx)
	if err != nil || c == nil {
		return
	}
	internalHost := firstNonEmpty(s.getSetting(ctx, "cf_internal_host"), localLANIP())
	if internalHost == "" {
		return
	}
	for _, rt := range routes {
		s.cfApplyRoute(ctx, c, serverID, rt, internalHost)
	}
}

// cfApplyRoute does one hostname.
func (s *Server) cfApplyRoute(ctx context.Context, c *cloudflare.Client, serverID string, rt serverRoute, internalHost string) {
	domain := s.cfFullDomain(ctx, rt.Hostname)
	if domain == "" {
		return
	}
	port := s.serverRoutePort(ctx, serverID, rt.PortName)
	if port == 0 {
		return // UDP-only, no such port, or nothing to proxy
	}

	// If this route's hostname changed, the old one is still recorded — tear it
	// down first so we do not leave an orphan ingress rule / DNS record behind.
	if old := s.routeProvisionedCF(ctx, serverID, rt); old != "" && !strings.EqualFold(old, domain) {
		if zid, zerr := c.ZoneForHost(old); zerr == nil && zid != "" {
			c.SetZoneID(zid)
		}
		_ = c.RemoveHostname(old)
		_ = c.RemoveDNS(old)
	}

	// Manage the CNAME in the hostname's OWN zone so one tunnel can serve several
	// different root domains, not just subdomains of cf_base_domain. Falls back to
	// the client's default (base-domain) zone when the account owns no match.
	if zid, zerr := c.ZoneForHost(domain); zerr == nil && zid != "" {
		c.SetZoneID(zid)
	}
	service := fmt.Sprintf("http://%s:%d", internalHost, port)
	if err := c.UpsertHostname(domain, service); err != nil {
		return // ingress is the critical part; don't record a half-applied state
	}
	// DNS is non-fatal (may be managed manually). A foreign-tunnel conflict is
	// surfaced loudly so a hostname another node serves isn't silently hijacked.
	if err := c.EnsureDNS(domain); errors.Is(err, cloudflare.ErrForeignTunnel) {
		log.Printf("cfAddServer: %q already CNAMEs to a different Cloudflare tunnel — DNS left untouched to avoid hijacking it (ingress on this tunnel was still added)", domain)
	}
	s.setRouteProvisionedCF(ctx, serverID, rt, domain)
}

// routeProvisionedCF / setRouteProvisionedCF read and write the hostname actually
// provisioned for a route. The primary keeps living on the servers row, where it
// always has, so no existing install's state has to move.
func (s *Server) routeProvisionedCF(ctx context.Context, serverID string, rt serverRoute) string {
	var host string
	if rt.Primary {
		s.db.QueryRowContext(ctx, "SELECT COALESCE(cf_hostname,'') FROM servers WHERE id=?", serverID).Scan(&host)
	} else {
		s.db.QueryRowContext(ctx, "SELECT COALESCE(cf_hostname,'') FROM server_routes WHERE id=?", rt.ID).Scan(&host)
	}
	return host
}

func (s *Server) setRouteProvisionedCF(ctx context.Context, serverID string, rt serverRoute, host string) {
	if rt.Primary {
		s.db.ExecContext(ctx, "UPDATE servers SET cf_hostname=? WHERE id=?", host, serverID)
		return
	}
	s.db.ExecContext(ctx, "UPDATE server_routes SET cf_hostname=? WHERE id=?", host, rt.ID)
}

// cfProvisionedHosts lists every hostname currently provisioned for a server,
// primary and extra. Teardown reads THIS rather than the configured hostnames:
// what has to be removed is what was created, which is not the same thing the
// moment somebody edits a hostname.
func (s *Server) cfProvisionedHosts(ctx context.Context, serverID string) []string {
	var out []string
	var primary string
	s.db.QueryRowContext(ctx, "SELECT COALESCE(cf_hostname,'') FROM servers WHERE id=?", serverID).Scan(&primary)
	if primary != "" {
		out = append(out, primary)
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT COALESCE(cf_hostname,'') FROM server_routes WHERE server_id=? AND COALESCE(cf_hostname,'')<>''", serverID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if rows.Scan(&h) == nil && h != "" {
			out = append(out, h)
		}
	}
	return out
}

// cfRemoveServer deletes every tunnel ingress rule + CNAME this server owns
// (best-effort) and clears the recorded hostnames.
func (s *Server) cfRemoveServer(serverID string) {
	defer recoverLog("cfRemoveServer")
	ctx := context.Background()
	hosts := s.cfProvisionedHosts(ctx, serverID)
	if len(hosts) == 0 {
		return
	}
	c, err := s.cfClient(ctx)
	if err != nil || c == nil {
		return
	}
	for _, host := range hosts {
		if zid, zerr := c.ZoneForHost(host); zerr == nil && zid != "" {
			c.SetZoneID(zid) // remove the CNAME from the hostname's own zone
		}
		_ = c.RemoveHostname(host)
		_ = c.RemoveDNS(host)
	}
	s.db.ExecContext(ctx, "UPDATE servers SET cf_hostname='' WHERE id=?", serverID)
	s.db.ExecContext(ctx, "UPDATE server_routes SET cf_hostname='' WHERE server_id=?", serverID)
}

// dropRouteHost tears down one extra route, used when it is deleted.
func (s *Server) dropRouteHost(ctx context.Context, routeID string) {
	defer recoverLog("dropRouteHost")
	var host string
	var npmID int
	s.db.QueryRowContext(ctx,
		"SELECT COALESCE(cf_hostname,''), COALESCE(npm_host_id,0) FROM server_routes WHERE id=?",
		routeID).Scan(&host, &npmID)
	if host != "" {
		if c, err := s.cfClient(ctx); err == nil && c != nil {
			if zid, zerr := c.ZoneForHost(host); zerr == nil && zid != "" {
				c.SetZoneID(zid)
			}
			_ = c.RemoveHostname(host)
			_ = c.RemoveDNS(host)
		}
	}
	if npmID != 0 {
		if c, err := s.npmClient(ctx); err == nil && c != nil {
			_ = c.DeleteProxyHost(npmID)
		}
	}
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
	// 1) Zone access (DNS) — resolve from the base domain when no zone id is set.
	if zone == "" {
		if base == "" {
			jsonError(w, "base domain (or zone ID) is required", http.StatusBadRequest)
			return
		}
		resolved, err := c.ResolveZoneID(base)
		if err != nil {
			jsonError(w, "couldn't read the zone for "+base+" — the token needs Zone → DNS: Edit (and Zone: Read). ("+err.Error()+")", http.StatusBadGateway)
			return
		}
		zone = resolved
		c.SetZoneID(resolved)
		s.setSetting(r.Context(), "cf_zone_id", resolved)
	}
	// 2) Tunnel access — confirms Account → Cloudflare Tunnel: Edit + correct ids.
	if err := c.CheckTunnel(); err != nil {
		jsonError(w, "DNS/zone OK, but the tunnel isn't reachable — check the Account ID + Tunnel ID and give the token Account → Cloudflare Tunnel: Edit. ("+err.Error()+")", http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]any{"ok": true, "zone_id": zone})
}
