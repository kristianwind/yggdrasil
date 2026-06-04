// Package cloudflare is a minimal client for the Cloudflare API, used to route a
// subdomain to a local game/app server through a *remotely-managed* Cloudflare
// Tunnel. It manages two things per server:
//
//  1. a tunnel ingress rule (hostname → http://<internal_host>:<port>), via
//     GET/PUT /accounts/{acct}/cfd_tunnel/{tunnel}/configurations; and
//  2. a proxied CNAME DNS record (hostname → <tunnel-id>.cfargotunnel.com), via
//     /zones/{zone}/dns_records.
//
// Auth is a single API token (Bearer) — no session/CSRF, so it's stateless and
// simpler than the UniFi/NPM clients. The cloudflared connector daemon must be
// running and bound to this tunnel for traffic to actually flow; configuring the
// ingress/DNS here works regardless of whether the connector is currently up.
package cloudflare

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const apiBase = "https://api.cloudflare.com/client/v4"

type Client struct {
	token     string
	accountID string
	zoneID    string
	tunnelID  string
	hc        *http.Client
}

// New builds a client. zoneID may be empty and resolved later via ResolveZoneID.
func New(token, accountID, zoneID, tunnelID string) *Client {
	return &Client{
		token:     strings.TrimSpace(token),
		accountID: strings.TrimSpace(accountID),
		zoneID:    strings.TrimSpace(zoneID),
		tunnelID:  strings.TrimSpace(tunnelID),
		hc:        &http.Client{Timeout: 15 * time.Second},
	}
}

// envelope is Cloudflare's standard API response wrapper.
type envelope struct {
	Success bool            `json:"success"`
	Errors  []cfError       `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e envelope) err() error {
	if e.Success {
		return nil
	}
	if len(e.Errors) > 0 {
		return fmt.Errorf("cloudflare: %s (code %d)", e.Errors[0].Message, e.Errors[0].Code)
	}
	return fmt.Errorf("cloudflare: request failed")
}

// do sends a request and unwraps the envelope; result is decoded into out.
func (c *Client) do(method, path string, in, out any) error {
	var rdr io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, apiBase+path, rdr)
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env envelope
	if e := json.Unmarshal(b, &env); e != nil {
		return fmt.Errorf("cloudflare %s %s: HTTP %d (bad response)", method, path, resp.StatusCode)
	}
	if err := env.err(); err != nil {
		return err
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// Verify confirms the API token is valid (used by the settings Test button).
func (c *Client) Verify() error {
	return c.do("GET", "/user/tokens/verify", nil, nil)
}

// ResolveZoneID looks up the zone id for a domain (e.g. "example.com"). Handy so
// the user only has to supply the base domain, not the internal zone id.
func (c *Client) ResolveZoneID(name string) (string, error) {
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.do("GET", "/zones?name="+url.QueryEscape(strings.ToLower(name)), nil, &zones); err != nil {
		return "", err
	}
	if len(zones) == 0 {
		return "", fmt.Errorf("no Cloudflare zone found for %q", name)
	}
	return zones[0].ID, nil
}

// ZoneID returns the (possibly resolved) zone id.
func (c *Client) ZoneID() string { return c.zoneID }

// SetZoneID overrides the zone id (after ResolveZoneID).
func (c *Client) SetZoneID(id string) { c.zoneID = id }

// cfTarget is the tunnel's stable CNAME target.
func (c *Client) cfTarget() string { return c.tunnelID + ".cfargotunnel.com" }

// --- Tunnel ingress ---

// getConfig returns the tunnel's current config object (ingress + any other
// keys), preserved as a map so we don't drop fields we don't manage on PUT.
func (c *Client) getConfig() (map[string]any, error) {
	var res struct {
		Config map[string]any `json:"config"`
	}
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", c.accountID, c.tunnelID)
	if err := c.do("GET", path, nil, &res); err != nil {
		return nil, err
	}
	if res.Config == nil {
		res.Config = map[string]any{}
	}
	return res.Config, nil
}

func (c *Client) putConfig(cfg map[string]any) error {
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", c.accountID, c.tunnelID)
	return c.do("PUT", path, map[string]any{"config": cfg}, nil)
}

// ingressRules extracts the current ingress list as []map[string]any.
func ingressRules(cfg map[string]any) []map[string]any {
	raw, _ := cfg["ingress"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// rebuildIngress writes hostname rules back with a trailing catch-all 404 (the
// catch-all is mandatory and must be last, or Cloudflare rejects the config).
func rebuildIngress(cfg map[string]any, rules []map[string]any) {
	cfg["ingress"] = append(rules, map[string]any{"service": "http_status:404"})
}

// UpsertHostname adds or replaces the ingress rule for hostname → service.
func (c *Client) UpsertHostname(hostname, service string) error {
	cfg, err := c.getConfig()
	if err != nil {
		return err
	}
	var kept []map[string]any
	for _, r := range ingressRules(cfg) {
		h, _ := r["hostname"].(string)
		if h == "" {
			continue // drop the old catch-all; rebuildIngress re-adds it
		}
		if strings.EqualFold(h, hostname) {
			continue // replace ours
		}
		kept = append(kept, r)
	}
	kept = append(kept, map[string]any{"hostname": hostname, "service": service})
	rebuildIngress(cfg, kept)
	return c.putConfig(cfg)
}

// RemoveHostname deletes the ingress rule for hostname (no-op if absent).
func (c *Client) RemoveHostname(hostname string) error {
	cfg, err := c.getConfig()
	if err != nil {
		return err
	}
	var kept []map[string]any
	for _, r := range ingressRules(cfg) {
		h, _ := r["hostname"].(string)
		if h == "" || strings.EqualFold(h, hostname) {
			continue
		}
		kept = append(kept, r)
	}
	rebuildIngress(cfg, kept)
	return c.putConfig(cfg)
}

// --- DNS ---

type dnsRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

func (c *Client) findDNS(hostname string) (*dnsRecord, error) {
	if c.zoneID == "" {
		return nil, fmt.Errorf("cloudflare: zone id not set")
	}
	var recs []dnsRecord
	path := fmt.Sprintf("/zones/%s/dns_records?name=%s", c.zoneID, url.QueryEscape(strings.ToLower(hostname)))
	if err := c.do("GET", path, nil, &recs); err != nil {
		return nil, err
	}
	for i := range recs {
		if strings.EqualFold(recs[i].Name, hostname) {
			return &recs[i], nil
		}
	}
	return nil, nil
}

// EnsureDNS creates or updates a proxied CNAME hostname → <tunnel>.cfargotunnel.com.
func (c *Client) EnsureDNS(hostname string) error {
	rec, err := c.findDNS(hostname)
	if err != nil {
		return err
	}
	body := map[string]any{
		"type":    "CNAME",
		"name":    hostname,
		"content": c.cfTarget(),
		"proxied": true,
		"ttl":     1,
	}
	if rec == nil {
		return c.do("POST", fmt.Sprintf("/zones/%s/dns_records", c.zoneID), body, nil)
	}
	return c.do("PUT", fmt.Sprintf("/zones/%s/dns_records/%s", c.zoneID, rec.ID), body, nil)
}

// RemoveDNS deletes the CNAME for hostname (no-op if absent).
func (c *Client) RemoveDNS(hostname string) error {
	rec, err := c.findDNS(hostname)
	if err != nil || rec == nil {
		return err
	}
	return c.do("DELETE", fmt.Sprintf("/zones/%s/dns_records/%s", c.zoneID, rec.ID), nil, nil)
}
