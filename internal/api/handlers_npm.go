package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/kristianwind/yggdrasil/internal/npm"
)

// normalizeSubdomain lowercases and trims a user-supplied subdomain label (or a
// full custom domain). Empty = the feature is off for that server.
func normalizeSubdomain(s string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(s)), ".")
}

// npmClient builds and logs in an NPM client from stored settings, or returns
// nil if NPM isn't configured/enabled.
func (s *Server) npmClient(ctx context.Context) (*npm.Client, error) {
	if s.getSetting(ctx, "npm_enabled") != "1" {
		return nil, nil
	}
	url := s.getSetting(ctx, "npm_url")
	email := s.getSetting(ctx, "npm_email")
	encPass := s.getSetting(ctx, "npm_password")
	if url == "" || email == "" || encPass == "" {
		return nil, nil
	}
	pass, err := s.cipher.Decrypt(encPass)
	if err != nil {
		return nil, fmt.Errorf("decrypt NPM password: %w", err)
	}
	c := npm.New(url, email, pass)
	if err := c.Login(); err != nil {
		return nil, err
	}
	return c, nil
}

// npmFullDomain resolves a server's subdomain setting to a full domain. A value
// containing a dot is treated as a full custom domain; otherwise it's a label
// joined to the configured base domain. Returns "" when not resolvable.
func (s *Server) npmFullDomain(ctx context.Context, sub string) string {
	sub = normalizeSubdomain(sub)
	if sub == "" {
		return ""
	}
	if strings.Contains(sub, ".") {
		return sub // full custom domain
	}
	base := normalizeSubdomain(s.getSetting(ctx, "npm_base_domain"))
	if base == "" {
		return ""
	}
	return sub + "." + base
}

// serverWebPort returns the host port to proxy to: the port named "web", else
// the first tcp port. Returns 0 if the server has no suitable (tcp) port.
func (s *Server) serverWebPort(ctx context.Context, serverID string) int {
	rt, err := s.loadRuntime(ctx, serverID)
	if err != nil {
		return 0
	}
	var firstTCP int
	for _, p := range rt.gs.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		if proto != "tcp" {
			continue
		}
		hp := rt.ports[p.Name]
		if hp <= 0 {
			continue
		}
		if p.Name == "web" {
			return hp
		}
		if firstTCP == 0 {
			firstTCP = hp
		}
	}
	return firstTCP
}

// npmAddServer creates (or refreshes) an NPM proxy host for every hostname the
// server claims (best-effort; one failing route does not stop the others).
func (s *Server) npmAddServer(serverID, serverName string) {
	defer recoverLog("npmAddServer")
	ctx := context.Background()

	routes := s.serverRoutes(ctx, serverID)
	if len(routes) == 0 {
		return
	}
	c, err := s.npmClient(ctx)
	if err != nil || c == nil {
		return
	}
	internalHost := firstNonEmpty(s.getSetting(ctx, "npm_internal_host"), localLANIP())
	if internalHost == "" {
		return
	}
	for _, rt := range routes {
		s.npmApplyRoute(ctx, c, serverID, rt, internalHost)
	}
}

// npmApplyRoute does one hostname.
func (s *Server) npmApplyRoute(ctx context.Context, c *npm.Client, serverID string, rt serverRoute, internalHost string) {
	domain := s.npmFullDomain(ctx, rt.Hostname)
	if domain == "" {
		return
	}
	port := s.serverRoutePort(ctx, serverID, rt.PortName)
	if port == 0 {
		return // UDP-only / no such port → nothing to proxy
	}

	// Reconcile: drop any existing host for this domain so we don't collide on the
	// domain_names uniqueness (NPM rejects overlaps), then any host we previously
	// recorded for THIS route (covers a hostname change to a different domain).
	npmDeleteByDomain(c, domain)
	if oldID := s.routeProvisionedNPM(ctx, serverID, rt); oldID != 0 {
		_ = c.DeleteProxyHost(oldID)
	}

	id, err := c.CreateProxyHost(domain, internalHost, port, npm.CreateOpts{
		LEEmail: s.getSetting(ctx, "npm_le_email"),
	})
	if err != nil {
		// LE cert issuance can fail (HTTP-01 needs port 80 reachable for the domain).
		// NPM may leave a partial host behind on that failure, so clean it up first,
		// then retry without SSL so routing still works (add a wildcard cert later).
		npmDeleteByDomain(c, domain)
		id, err = c.CreateProxyHost(domain, internalHost, port, npm.CreateOpts{NoSSL: true})
		if err != nil {
			return
		}
	}
	// Recover the id from the host list if the create response didn't carry one, so
	// the stored id is always precise for later deletes.
	if id == 0 {
		id = npmHostIDForDomain(c, domain)
	}
	s.setRouteProvisionedNPM(ctx, serverID, rt, id)
}

func (s *Server) routeProvisionedNPM(ctx context.Context, serverID string, rt serverRoute) int {
	var id int
	if rt.Primary {
		s.db.QueryRowContext(ctx, "SELECT COALESCE(npm_host_id,0) FROM servers WHERE id=?", serverID).Scan(&id)
	} else {
		s.db.QueryRowContext(ctx, "SELECT COALESCE(npm_host_id,0) FROM server_routes WHERE id=?", rt.ID).Scan(&id)
	}
	return id
}

func (s *Server) setRouteProvisionedNPM(ctx context.Context, serverID string, rt serverRoute, id int) {
	if rt.Primary {
		s.db.ExecContext(ctx, "UPDATE servers SET npm_host_id=? WHERE id=?", id, serverID)
		return
	}
	s.db.ExecContext(ctx, "UPDATE server_routes SET npm_host_id=? WHERE id=?", id, rt.ID)
}

// npmDeleteByDomain removes every proxy host whose domain_names contain domain.
func npmDeleteByDomain(c *npm.Client, domain string) {
	if id := npmHostIDForDomain(c, domain); id != 0 {
		_ = c.DeleteProxyHost(id)
		// More than one may match (e.g. a partial LE residue); sweep again.
		if again := npmHostIDForDomain(c, domain); again != 0 {
			_ = c.DeleteProxyHost(again)
		}
	}
}

// npmHostIDForDomain returns the id of a proxy host serving domain, or 0.
func npmHostIDForDomain(c *npm.Client, domain string) int {
	hosts, err := c.ListProxyHosts()
	if err != nil {
		return 0
	}
	for _, h := range hosts {
		for _, d := range h.DomainNames {
			if strings.EqualFold(d, domain) {
				return h.ID
			}
		}
	}
	return 0
}

// npmRemoveServer deletes every NPM proxy host this server owns (best-effort)
// and clears the stored ids. Like the Cloudflare side, this reads what was
// PROVISIONED rather than what is configured — they differ the moment somebody
// edits a hostname, and the provisioned one is what actually needs removing.
func (s *Server) npmRemoveServer(serverID string) {
	defer recoverLog("npmRemoveServer")
	ctx := context.Background()

	ids := []int{}
	var primary int
	s.db.QueryRowContext(ctx, "SELECT COALESCE(npm_host_id,0) FROM servers WHERE id=?", serverID).Scan(&primary)
	if primary != 0 {
		ids = append(ids, primary)
	}
	if rows, err := s.db.QueryContext(ctx,
		"SELECT COALESCE(npm_host_id,0) FROM server_routes WHERE server_id=? AND COALESCE(npm_host_id,0)<>0",
		serverID); err == nil {
		for rows.Next() {
			var id int
			if rows.Scan(&id) == nil && id != 0 {
				ids = append(ids, id)
			}
		}
		rows.Close()
	}
	if len(ids) == 0 {
		return
	}
	c, err := s.npmClient(ctx)
	if err != nil || c == nil {
		return
	}
	for _, id := range ids {
		_ = c.DeleteProxyHost(id)
	}
	s.db.ExecContext(ctx, "UPDATE servers SET npm_host_id=0 WHERE id=?", serverID)
	s.db.ExecContext(ctx, "UPDATE server_routes SET npm_host_id=0 WHERE server_id=?", serverID)
}

// --- Settings endpoints ---

func (s *Server) handleGetNpmSettings(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"url":           s.getSetting(r.Context(), "npm_url"),
		"email":         s.getSetting(r.Context(), "npm_email"),
		"base_domain":   s.getSetting(r.Context(), "npm_base_domain"),
		"internal_host": s.getSetting(r.Context(), "npm_internal_host"),
		"le_email":      s.getSetting(r.Context(), "npm_le_email"),
		"enabled":       s.getSetting(r.Context(), "npm_enabled") == "1",
		"configured":    s.getSetting(r.Context(), "npm_password") != "",
	})
}

func (s *Server) handleSetNpmSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL          string `json:"url"`
		Email        string `json:"email"`
		Password     string `json:"password"` // blank = keep existing
		BaseDomain   string `json:"base_domain"`
		InternalHost string `json:"internal_host"`
		LEEmail      string `json:"le_email"`
		Enabled      bool   `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	url := strings.TrimRight(strings.TrimSpace(req.URL), "/")
	if url != "" && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "http://" + url
	}
	s.setSetting(r.Context(), "npm_url", url)
	s.setSetting(r.Context(), "npm_email", strings.TrimSpace(req.Email))
	s.setSetting(r.Context(), "npm_base_domain", normalizeSubdomain(req.BaseDomain))
	s.setSetting(r.Context(), "npm_internal_host", strings.TrimSpace(req.InternalHost))
	s.setSetting(r.Context(), "npm_le_email", strings.TrimSpace(req.LEEmail))
	s.setSetting(r.Context(), "npm_enabled", boolStr(req.Enabled))
	if req.Password != "" {
		if enc, err := s.cipher.Encrypt(req.Password); err == nil {
			s.setSetting(r.Context(), "npm_password", enc)
		}
	}
	s.auditLog(r, "settings.npm", "npm", map[string]any{"url": url, "enabled": req.Enabled})
	s.handleGetNpmSettings(w, r)
}

// handleTestNpm logs in and lists proxy hosts to confirm the configuration works.
// Uses the provided password if present, else the stored one.
func (s *Server) handleTestNpm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL      string `json:"url"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	decodeJSON(r, &req)
	url := firstNonEmpty(strings.TrimRight(strings.TrimSpace(req.URL), "/"), s.getSetting(r.Context(), "npm_url"))
	email := firstNonEmpty(strings.TrimSpace(req.Email), s.getSetting(r.Context(), "npm_email"))
	pass := req.Password
	if pass == "" {
		if enc := s.getSetting(r.Context(), "npm_password"); enc != "" {
			pass, _ = s.cipher.Decrypt(enc)
		}
	}
	if url == "" || email == "" || pass == "" {
		jsonError(w, "URL, email and password are required", http.StatusBadRequest)
		return
	}
	c := npm.New(url, email, pass)
	if err := c.Login(); err != nil {
		jsonError(w, "connection failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	hosts, err := c.ListProxyHosts()
	if err != nil {
		jsonError(w, "logged in, but listing proxy hosts failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]any{"ok": true, "hosts": len(hosts)})
}
