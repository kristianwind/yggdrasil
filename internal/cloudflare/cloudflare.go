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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrForeignTunnel signals that a hostname already CNAMEs to a DIFFERENT
// Cloudflare tunnel. With one tunnel per node, blindly overwriting it would
// hijack a hostname another node serves, so we refuse and let the caller warn.
var ErrForeignTunnel = errors.New("hostname already routed to a different Cloudflare tunnel")

const apiBase = "https://api.cloudflare.com/client/v4"

type Client struct {
	token     string
	accountID string
	zoneID    string
	tunnelID  string
	// homeZone is the panel's default zone (cf_base_domain's). UseZoneFor falls
	// back to it — never to whichever zone the previous hostname happened to
	// leave behind on this client.
	homeZone string
	// zones caches the account's zone list for the client's lifetime. A client
	// lives for one provisioning pass, so this turns "one /zones call per
	// hostname" into one per pass without ever going stale for long.
	zones []zoneRef

	// Logf, when set, is told about every change this client makes to DNS or to
	// the tunnel's ingress. Nothing else in the panel records those writes, so
	// without it a deleted record leaves no trace of which code removed it —
	// measured on 2026-10-20, when app.getcellarius.com vanished for eight hours
	// after a restart and the journal had not one line about it.
	Logf func(format string, args ...any)
	hc   *http.Client
	// baseURL is apiBase in production. It exists so the read-modify-write on
	// the tunnel config can be exercised against a real HTTP server in tests —
	// the concurrency bug it now guards cannot be reproduced with a stub.
	baseURL string
}

// New builds a client. zoneID may be empty and resolved later via ResolveZoneID.
func New(token, accountID, zoneID, tunnelID string) *Client {
	return &Client{
		token:     strings.TrimSpace(token),
		accountID: strings.TrimSpace(accountID),
		zoneID:    strings.TrimSpace(zoneID),
		homeZone:  strings.TrimSpace(zoneID),
		tunnelID:  strings.TrimSpace(tunnelID),
		hc:        &http.Client{Timeout: 15 * time.Second},
		baseURL:   apiBase,
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
	req, _ := http.NewRequest(method, c.baseURL+path, rdr)
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

// CheckTunnel confirms the token can reach this tunnel's config — i.e. it carries
// the Account → Cloudflare Tunnel: Edit permission for the given account/tunnel.
// (We deliberately don't use /user/tokens/verify: account-scoped tokens can't call
// /user/* and it returns a misleading "Invalid API Token".)
func (c *Client) CheckTunnel() error {
	_, err := c.getConfig()
	return err
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

// ZoneForHost finds the account zone that owns hostname — the longest zone-name
// suffix match against the account's actual zones. This lets one panel/tunnel
// route several *different* root domains (not just subdomains of one base): DNS
// records land in the correct zone per hostname. Returns "" (no error) when the
// account owns no matching zone, so the caller can fall back to manual DNS.
func (c *Client) ZoneForHost(hostname string) (string, error) {
	hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	if hostname == "" {
		return "", nil
	}
	if c.zones == nil {
		var zones []zoneRef
		if err := c.do("GET", "/zones?per_page=200", nil, &zones); err != nil {
			return "", err
		}
		if zones == nil {
			zones = []zoneRef{}
		}
		c.zones = zones
	}
	bestID, bestLen := "", 0
	for _, z := range c.zones {
		zn := strings.ToLower(z.Name)
		if (hostname == zn || strings.HasSuffix(hostname, "."+zn)) && len(zn) > bestLen {
			bestID, bestLen = z.ID, len(zn)
		}
	}
	return bestID, nil
}

type zoneRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// UseZoneFor points the client at hostname's own zone, or at the home zone when
// the account owns no zone matching it.
//
// A failed lookup is an error and the zone is left UNSET rather than as it was.
// The pattern this replaces — `if zid, err := ZoneForHost(h); err == nil {
// SetZoneID(zid) }` — kept the previous hostname's zone on any API hiccup, so
// one server's routes could be written to, or deleted from, the zone of the
// route before it.
func (c *Client) UseZoneFor(hostname string) error {
	zid, err := c.ZoneForHost(hostname)
	if err != nil {
		c.zoneID = ""
		return err
	}
	if zid == "" {
		zid = c.homeZone
	}
	c.zoneID = zid
	return nil
}

// ZoneID returns the (possibly resolved) zone id.
func (c *Client) ZoneID() string { return c.zoneID }

// SetZoneID sets the home zone (after ResolveZoneID) and selects it.
func (c *Client) SetZoneID(id string) { c.zoneID, c.homeZone = id, id }

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// cfTarget is the tunnel's stable CNAME target.
func (c *Client) cfTarget() string { return c.tunnelID + ".cfargotunnel.com" }

// --- Tunnel ingress ---
//
// Every edit below is a read-modify-write of ONE shared document: fetch the
// tunnel's whole config, change one rule, PUT it back. Two of those in flight at
// once means the second PUT is built from a copy fetched before the first one
// landed, so the first edit is silently undone. Nothing errors; the rule is
// simply not there afterwards.
//
// That is not theoretical. On 2026-09-16 a reconciler tore down several servers
// at once, each in its own goroutine: eleven DNS records went (independent
// per-record DELETEs, so all of them landed) and only seven ingress rules did.
// The four that survived were the lost writes — hostnames still routed by the
// tunnel with no DNS pointing at them.
//
// So the config is edited under a per-tunnel lock. It serialises only this
// process; two panels pointed at the same tunnel would still race, which is its
// own misconfiguration and is what cfTunnelWarning is for.
var ingressLocks sync.Map // tunnelID -> *sync.Mutex

func (c *Client) lockIngress() func() {
	v, _ := ingressLocks.LoadOrStore(c.tunnelID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

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

// IngressHostname is one hostname → service mapping from the tunnel config.
type IngressHostname struct {
	Hostname string `json:"hostname"`
	Service  string `json:"service"`
	Path     string `json:"path,omitempty"`
}

// ListIngress returns every hostname rule on this tunnel — including ones added
// directly in the Cloudflare dashboard, not just those the panel provisioned.
// The mandatory trailing catch-all (no hostname) is omitted.
func (c *Client) ListIngress() ([]IngressHostname, error) {
	cfg, err := c.getConfig()
	if err != nil {
		return nil, err
	}
	out := []IngressHostname{}
	for _, r := range ingressRules(cfg) {
		h, _ := r["hostname"].(string)
		if h == "" {
			continue // the catch-all rule carries no hostname
		}
		svc, _ := r["service"].(string)
		p, _ := r["path"].(string)
		out = append(out, IngressHostname{Hostname: h, Service: svc, Path: p})
	}
	return out, nil
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
	defer c.lockIngress()()
	cfg, err := c.getConfig()
	if err != nil {
		return err
	}
	var kept []map[string]any
	var had []string
	for _, r := range ingressRules(cfg) {
		h, _ := r["hostname"].(string)
		if h == "" {
			continue // drop the old catch-all; rebuildIngress re-adds it
		}
		if strings.EqualFold(h, hostname) {
			svc, _ := r["service"].(string)
			had = append(had, svc)
			continue // replace ours
		}
		kept = append(kept, r)
	}
	if len(had) == 1 && had[0] == service {
		return nil // already exactly this — a PUT would change nothing
	}
	kept = append(kept, map[string]any{"hostname": hostname, "service": service})
	rebuildIngress(cfg, kept)
	if err := c.putConfig(cfg); err != nil {
		return err
	}
	if len(had) == 0 {
		c.logf("cloudflare: added tunnel route %s -> %s", hostname, service)
	} else {
		c.logf("cloudflare: changed tunnel route %s from %v to %s", hostname, had, service)
	}
	return nil
}

// RemoveHostname deletes the ingress rule for hostname (no-op if absent).
func (c *Client) RemoveHostname(hostname string) error {
	defer c.lockIngress()()
	cfg, err := c.getConfig()
	if err != nil {
		return err
	}
	var kept []map[string]any
	removed := false
	for _, r := range ingressRules(cfg) {
		h, _ := r["hostname"].(string)
		if h == "" {
			continue
		}
		if strings.EqualFold(h, hostname) {
			removed = true
			continue
		}
		kept = append(kept, r)
	}
	if !removed {
		return nil // nothing to remove; a PUT would only rewrite the same config
	}
	rebuildIngress(cfg, kept)
	if err := c.putConfig(cfg); err != nil {
		return err
	}
	c.logf("cloudflare: removed tunnel route %s", hostname)
	return nil
}

// --- Firewall (IP Access Rules) ---
//
// These use Cloudflare's zone-level "IP Access Rules"
// (/zones/{zone}/firewall/access_rules/rules) to block a single client IP at the
// edge, so an attacker never reaches the origin. It's the simplest edge block
// that needs only the API token we already hold (Zone → Firewall Services: Edit).
// Rules are scoped to the zone that owns the attacked hostname.

type accessRule struct {
	ID            string `json:"id"`
	Mode          string `json:"mode"`
	Notes         string `json:"notes"`
	Configuration struct {
		Target string `json:"target"`
		Value  string `json:"value"`
	} `json:"configuration"`
}

// BlockIP creates a zone-level "block" access rule for a single IP and returns
// the created rule's id (needed to remove it later). If an identical rule already
// exists (Cloudflare rejects duplicates), it looks the existing one up and returns
// its id instead of erroring, so the call is idempotent.
func (c *Client) BlockIP(zoneID, ip, notes string) (string, error) {
	zoneID = strings.TrimSpace(zoneID)
	if zoneID == "" {
		return "", fmt.Errorf("cloudflare: zone id required to block an IP")
	}
	body := map[string]any{
		"mode":  "block",
		"notes": notes,
		"configuration": map[string]string{
			"target": accessRuleTarget(ip),
			"value":  ip,
		},
	}
	var out accessRule
	err := c.do("POST", fmt.Sprintf("/zones/%s/firewall/access_rules/rules", zoneID), body, &out)
	if err != nil {
		// A duplicate rule (same target/value) already blocks this IP — treat as success.
		if id, lerr := c.findAccessRule(zoneID, ip); lerr == nil && id != "" {
			return id, nil
		}
		return "", err
	}
	return out.ID, nil
}

// UnblockIP deletes a zone-level access rule by its id (no-op if already gone).
func (c *Client) UnblockIP(zoneID, ruleID string) error {
	zoneID, ruleID = strings.TrimSpace(zoneID), strings.TrimSpace(ruleID)
	if zoneID == "" || ruleID == "" {
		return fmt.Errorf("cloudflare: zone id and rule id required to unblock")
	}
	err := c.do("DELETE", fmt.Sprintf("/zones/%s/firewall/access_rules/rules/%s", zoneID, ruleID), nil, nil)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "not found") {
		return nil
	}
	return err
}

// findAccessRule returns the id of an existing block rule for ip in the zone, or
// "" if none. Used to make BlockIP idempotent when Cloudflare reports a duplicate.
func (c *Client) findAccessRule(zoneID, ip string) (string, error) {
	var rules []accessRule
	path := fmt.Sprintf("/zones/%s/firewall/access_rules/rules?configuration.target=%s&configuration.value=%s",
		zoneID, accessRuleTarget(ip), url.QueryEscape(ip))
	if err := c.do("GET", path, nil, &rules); err != nil {
		return "", err
	}
	for _, r := range rules {
		if strings.EqualFold(r.Configuration.Value, ip) {
			return r.ID, nil
		}
	}
	return "", nil
}

// accessRuleTarget picks Cloudflare's target type for an address: "ip6" for IPv6,
// "ip" for IPv4. (CIDR ranges would be "ip_range", but we only block single IPs.)
func accessRuleTarget(ip string) string {
	if strings.Contains(ip, ":") {
		return "ip6"
	}
	return "ip"
}

// --- DNS ---

type dnsRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

// FindDNSRecord reports whether a record for hostname exists in the current
// zone. Exported for reconciliation, which needs to know what is missing before
// it decides to write anything — an error is reported as "not found" only by
// the caller's choice, never here, so a Cloudflare outage cannot be mistaken
// for an absent record.
func (c *Client) FindDNSRecord(hostname string) (bool, error) {
	rec, err := c.findDNS(hostname, "CNAME", "A", "AAAA")
	if err != nil {
		return false, err
	}
	return rec != nil, nil
}

// findDNS returns the first record for hostname of one of the given types.
//
// 🔴 The type filter is not an optimisation — it is the whole safety property.
// A name in Cloudflare is a SET of records: at a zone apex that set contains
// the A or CNAME the panel manages and also the MX, TXT and SRV records that
// deliver the domain's mail. This function used to ask for the name alone and
// return whichever record came back first, and EnsureDNS then PUT a CNAME over
// that record id — converting, in place, whatever it happened to find.
//
// That destroyed live mail records on 2026-09-19: MX on executit.dk and
// 3dekoration.dk, SPF on nolimit.dk. Which record died was down to the order
// Cloudflare listed them in, so the same code was harmless for months and then
// ate a mail server. Every zone where a server's hostname is the apex was
// exposed, and the panel routes five of them.
//
// Never widen these types beyond records the panel actually owns.
func (c *Client) findDNS(hostname string, types ...string) (*dnsRecord, error) {
	if c.zoneID == "" {
		return nil, fmt.Errorf("cloudflare: zone id not set")
	}
	if len(types) == 0 {
		return nil, fmt.Errorf("cloudflare: findDNS called without a record type")
	}
	for _, t := range types {
		var recs []dnsRecord
		path := fmt.Sprintf("/zones/%s/dns_records?name=%s&type=%s",
			c.zoneID, url.QueryEscape(strings.ToLower(hostname)), url.QueryEscape(t))
		if err := c.do("GET", path, nil, &recs); err != nil {
			return nil, err
		}
		for i := range recs {
			// Belt and braces: the filter is server-side, but a record of the
			// wrong type reaching the PUT below is the failure mode that cost
			// the mail records, so it is checked here too.
			if strings.EqualFold(recs[i].Name, hostname) && strings.EqualFold(recs[i].Type, t) {
				return &recs[i], nil
			}
		}
	}
	return nil, nil
}

// CNAMEForeign reports whether hostname's proxied CNAME points at a DIFFERENT
// Cloudflare tunnel than this client's — a conflict, meaning some other node/
// tunnel owns the hostname. Returns the offending target for display. Absent, a
// non-CNAME, or a CNAME pointing at this tunnel all report foreign=false.
func (c *Client) CNAMEForeign(hostname string) (foreign bool, target string, err error) {
	// Resolve the hostname's own zone (multi-domain); fall back to the home zone.
	if err := c.UseZoneFor(hostname); err != nil {
		return false, "", err
	}
	rec, err := c.findDNS(hostname, "CNAME")
	if err != nil || rec == nil {
		return false, "", err
	}
	if strings.EqualFold(rec.Type, "CNAME") &&
		strings.HasSuffix(strings.ToLower(rec.Content), ".cfargotunnel.com") &&
		!strings.EqualFold(rec.Content, c.cfTarget()) {
		return true, rec.Content, nil
	}
	return false, "", nil
}

// EnsureDNS creates or updates a proxied CNAME hostname → <tunnel>.cfargotunnel.com.
// If a CNAME already points at a DIFFERENT tunnel it returns ErrForeignTunnel
// instead of overwriting it — so we never steal a hostname another node serves.
func (c *Client) EnsureDNS(hostname string) error {
	// CNAME first (what the panel normally owns), then the A/AAAA that a zone
	// apex carries instead. Nothing else is ever a candidate for replacement.
	rec, err := c.findDNS(hostname, "CNAME", "A", "AAAA")
	if err != nil {
		return err
	}
	if rec != nil && strings.EqualFold(rec.Type, "CNAME") &&
		strings.HasSuffix(strings.ToLower(rec.Content), ".cfargotunnel.com") &&
		!strings.EqualFold(rec.Content, c.cfTarget()) {
		return ErrForeignTunnel
	}
	if rec != nil && strings.EqualFold(rec.Type, "CNAME") && rec.Proxied &&
		strings.EqualFold(rec.Content, c.cfTarget()) {
		return nil // already correct
	}
	body := map[string]any{
		"type":    "CNAME",
		"name":    hostname,
		"content": c.cfTarget(),
		"proxied": true,
		"ttl":     1,
	}
	if rec == nil {
		if err := c.do("POST", fmt.Sprintf("/zones/%s/dns_records", c.zoneID), body, nil); err != nil {
			return err
		}
		c.logf("cloudflare: created DNS record %s -> %s (zone %s)", hostname, c.cfTarget(), c.zoneID)
		return nil
	}
	if err := c.do("PUT", fmt.Sprintf("/zones/%s/dns_records/%s", c.zoneID, rec.ID), body, nil); err != nil {
		return err
	}
	c.logf("cloudflare: replaced %s record %s (was %s) with a CNAME to %s (zone %s)",
		rec.Type, hostname, rec.Content, c.cfTarget(), c.zoneID)
	return nil
}

// RemoveDNS deletes the CNAME for hostname — but only when it points at THIS
// tunnel, so we never delete a record another node/tunnel owns (or an unrelated
// record a user manages by hand). No-op if absent or foreign.
func (c *Client) RemoveDNS(hostname string) error {
	// CNAME only. A tunnel record is always a CNAME, and asking without a type
	// used to return the name's MX instead — whose content never matches the
	// tunnel target, so removal silently did nothing and left the route behind.
	rec, err := c.findDNS(hostname, "CNAME")
	if err != nil || rec == nil {
		return err
	}
	if !strings.EqualFold(rec.Content, c.cfTarget()) {
		return nil // not ours — leave it alone
	}
	if err := c.do("DELETE", fmt.Sprintf("/zones/%s/dns_records/%s", c.zoneID, rec.ID), nil, nil); err != nil {
		return err
	}
	c.logf("cloudflare: deleted DNS record %s (zone %s)", hostname, c.zoneID)
	return nil
}
