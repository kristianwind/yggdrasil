package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/kristianwind/yggdrasil/internal/unifi"
)

// unifiClient builds and logs in a UniFi client from stored settings, or returns
// nil if UniFi isn't configured/enabled.
func (s *Server) unifiClient(ctx context.Context) (*unifi.Client, error) {
	if s.getSetting(ctx, "unifi_enabled") != "1" {
		return nil, nil
	}
	url := s.getSetting(ctx, "unifi_url")
	user := s.getSetting(ctx, "unifi_user")
	encPass := s.getSetting(ctx, "unifi_pass")
	if url == "" || user == "" || encPass == "" {
		return nil, nil
	}
	pass, err := s.cipher.Decrypt(encPass)
	if err != nil {
		return nil, fmt.Errorf("decrypt UniFi password: %w", err)
	}
	site := s.getSetting(ctx, "unifi_site")
	c := unifi.New(url, user, pass, site)
	if err := c.Login(); err != nil {
		return nil, err
	}
	return c, nil
}

// unifiRuleTag uniquely identifies a server's rules in the rule name.
func unifiRuleTag(serverID string) string {
	if len(serverID) > 8 {
		serverID = serverID[:8]
	}
	return "[ygg:" + serverID + "]"
}

// unifiAddServer creates WAN port-forward rules for a server (best-effort).
func (s *Server) unifiAddServer(serverID, serverName string) {
	defer recoverLog("unifiAddServer")
	ctx := context.Background()
	c, err := s.unifiClient(ctx)
	if err != nil || c == nil {
		return
	}
	s.unifiDeleteFor(c, serverID) // clear stale rules first
	lan := localLANIP()
	if lan == "" {
		return
	}
	tag := unifiRuleTag(serverID)
	for _, pp := range s.serverPortProtos(ctx, serverID) {
		if pp.Admin {
			continue // never WAN-forward the RCON/admin port
		}
		port := strconv.Itoa(pp.Port)
		_ = c.CreatePortForward(unifi.PortForward{
			Name:          fmt.Sprintf("Yggdrasil: %s %s", serverName, tag),
			Enabled:       true,
			Src:           "any",
			DstPort:       port,
			Fwd:           lan,
			FwdPort:       port,
			Proto:         unifiProto(pp.Proto),
			PfwdInterface: "wan",
		})
	}
}

// unifiRemoveServer removes a server's WAN port-forward rules (best-effort).
func (s *Server) unifiRemoveServer(serverID string) {
	defer recoverLog("unifiRemoveServer")
	c, err := s.unifiClient(context.Background())
	if err != nil || c == nil {
		return
	}
	s.unifiDeleteFor(c, serverID)
}

// unifiDeleteFor deletes all rules tagged with this server's id.
func (s *Server) unifiDeleteFor(c *unifi.Client, serverID string) {
	tag := unifiRuleTag(serverID)
	rules, err := c.ListPortForwards()
	if err != nil {
		return
	}
	for _, r := range rules {
		if strings.Contains(r.Name, tag) {
			_ = c.DeletePortForward(r.ID)
		}
	}
}

func unifiProto(p string) string {
	if p == "udp" || p == "UDP" {
		return "udp"
	}
	return "tcp"
}

// localLANIP returns this host's primary LAN IP (the forward target).
func localLANIP() string {
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

// --- Settings endpoints ---

func (s *Server) handleGetUnifiSettings(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"url":        s.getSetting(r.Context(), "unifi_url"),
		"username":   s.getSetting(r.Context(), "unifi_user"),
		"site":       firstNonEmpty(s.getSetting(r.Context(), "unifi_site"), "default"),
		"enabled":    s.getSetting(r.Context(), "unifi_enabled") == "1",
		"configured": s.getSetting(r.Context(), "unifi_pass") != "",
	})
}

func (s *Server) handleSetUnifiSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"` // blank = keep existing
		Site     string `json:"site"`
		Enabled  bool   `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	url := strings.TrimRight(strings.TrimSpace(req.URL), "/")
	if url != "" && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}
	s.setSetting(r.Context(), "unifi_url", url)
	s.setSetting(r.Context(), "unifi_user", strings.TrimSpace(req.Username))
	s.setSetting(r.Context(), "unifi_site", firstNonEmpty(strings.TrimSpace(req.Site), "default"))
	s.setSetting(r.Context(), "unifi_enabled", boolStr(req.Enabled))
	if req.Password != "" {
		if enc, err := s.cipher.Encrypt(req.Password); err == nil {
			s.setSetting(r.Context(), "unifi_pass", enc)
		}
	}
	s.auditLog(r, "settings.unifi", "unifi", map[string]any{"url": url, "enabled": req.Enabled})
	s.handleGetUnifiSettings(w, r)
}

// handleTestUnifi logs in and lists rules to confirm the configuration works.
// Uses the provided password if present, else the stored one.
func (s *Server) handleTestUnifi(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
		Site     string `json:"site"`
	}
	decodeJSON(r, &req)
	url := firstNonEmpty(strings.TrimRight(strings.TrimSpace(req.URL), "/"), s.getSetting(r.Context(), "unifi_url"))
	user := firstNonEmpty(strings.TrimSpace(req.Username), s.getSetting(r.Context(), "unifi_user"))
	site := firstNonEmpty(strings.TrimSpace(req.Site), s.getSetting(r.Context(), "unifi_site"))
	pass := req.Password
	if pass == "" {
		if enc := s.getSetting(r.Context(), "unifi_pass"); enc != "" {
			pass, _ = s.cipher.Decrypt(enc)
		}
	}
	if url == "" || user == "" || pass == "" {
		jsonError(w, "URL, username and password are required", http.StatusBadRequest)
		return
	}
	c := unifi.New(url, user, pass, site)
	if err := c.Login(); err != nil {
		jsonError(w, "connection failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	rules, err := c.ListPortForwards()
	if err != nil {
		jsonError(w, "logged in, but listing port forwards failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]any{"ok": true, "rules": len(rules)})
}
