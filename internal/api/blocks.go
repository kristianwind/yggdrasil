package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/kristianwind/yggdrasil/internal/cloudflare"
	"github.com/kristianwind/yggdrasil/internal/firewall"
)

// This file implements blocking an abusive client IP, either at Cloudflare's edge
// (for a hostname on a Cloudflare zone the panel's token can see) or in the host
// firewall via nftables (for a directly-exposed site that isn't behind a CDN).
//
// Enforcement point is chosen automatically: if a Cloudflare zone owns the attacked
// hostname, block there (the attacker never reaches the origin); otherwise fall back
// to the host firewall when the admin has enabled it. Blocks are recorded so they
// can be listed and reversed, and every block/unblock is audited and notified.
//
// Settings (app_settings KV):
//   block_enabled     "1"/"0"  — master switch for the whole feature (default off)
//   block_mode        off|propose|auto — how Kvasir treats a block_ip suggestion
//                     (default "propose": recorded + notified, admin applies)
//   block_nft_enabled "1"/"0"  — allow host-firewall (nftables) blocks (default off;
//                     needs CAP_NET_ADMIN on the service)

// cloudflareCIDRs are Cloudflare's published edge ranges. We refuse to block any of
// them (blocking Cloudflare itself would drop ALL proxied traffic), and they double
// as the allowlist the NPM CF-gate uses. Sourced from cloudflare.com/ips.
var cloudflareCIDRs = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
}

// neverBlockCIDRs are ranges we must never block: loopback, RFC1918 private,
// link-local, IPv6 ULA/link-local, and the Tailscale CGNAT range — plus Cloudflare
// (appended below). Blocking any of these would be pointless or self-harming
// (e.g. locking out the admin's own network or the CDN in front of the site).
var neverBlockCIDRs = func() []*net.IPNet {
	raw := append([]string{
		"127.0.0.0/8", "::1/128",
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"169.254.0.0/16", "fe80::/10", "fc00::/7",
		"100.64.0.0/10", // Tailscale / CGNAT
	}, cloudflareCIDRs...)
	nets := make([]*net.IPNet, 0, len(raw))
	for _, c := range raw {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// blockRefusal is a guard saying "not this address" — as opposed to an operational
// failure where the mechanism broke. Keeping them apart is the point: a refusal is
// evidence the detector was wrong, a failure is evidence the plumbing was, and a
// record that calls both "proposed" can measure neither.
//
// This mattered in the real data. Kvasir proposed blocking 198.41.192.37 twice on
// 2026-08-01 — that is Cloudflare's own edge, and blocking it would have dropped
// every proxied visitor to the site. The guard refused it correctly, and the event
// log recorded it identically to the eight legitimate suggestions, so nobody could
// see it had happened without checking the ranges by hand a month later.
type blockRefusal struct{ reason string }

func (e blockRefusal) Error() string { return e.reason }

func refuse(format string, a ...any) error { return blockRefusal{fmt.Sprintf(format, a...)} }

// isRefusal reports whether err came from a guard rather than from a failure.
func isRefusal(err error) bool {
	var r blockRefusal
	return errors.As(err, &r)
}

// checkBlockable validates an IP and refuses any address we must never block.
// Returns the normalized IP string on success, or a human-readable reason on refusal.
func checkBlockable(ip string) (string, error) {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return "", refuse("%q is not a valid IP address", ip)
	}
	for _, n := range neverBlockCIDRs {
		if n.Contains(parsed) {
			return "", refuse("refusing to block %s — it's in a protected range (%s)", parsed, n.String())
		}
	}
	return parsed.String(), nil
}

// operatorIPWindow is how far back a panel sign-in counts as "this is one of
// ours". Long enough to cover a home connection that only reaches the panel
// every few days, short enough that an address which changed hands is not
// protected forever.
const operatorIPWindow = "-14 days"

// recentOperatorIPs is the set of addresses an administrator has reached this
// panel from recently.
//
// Their own address is the one an attack-detector is most likely to misread:
// an admin clicking through a WordPress dashboard produces exactly the traffic
// shape of a scraper — many requests, one source, in a burst. This panel has
// already proposed blocking its owner's home address for doing that, and with
// block_mode=auto it would have carried it out.
func (s *Server) recentOperatorIPs(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	rows, err := s.db.QueryContext(ctx,
		"SELECT DISTINCT ip FROM audit_log WHERE COALESCE(ip,'')<>'' AND ts >= datetime('now', ?)", operatorIPWindow)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var ip string
		if rows.Scan(&ip) == nil {
			// Normalise through net.ParseIP so "1.2.3.4" and a padded or
			// alternate spelling of the same address compare equal.
			if p := net.ParseIP(strings.TrimSpace(ip)); p != nil {
				out[p.String()] = true
			}
		}
	}
	return out
}

// isOperatorIP reports whether ip belongs to someone who has signed in here.
func (s *Server) isOperatorIP(ctx context.Context, ip string) bool {
	p := net.ParseIP(strings.TrimSpace(ip))
	if p == nil {
		return false
	}
	return s.recentOperatorIPs(ctx)[p.String()]
}

// cfFirewallHint turns Cloudflare's "Authentication error (code 10000)" into
// something an operator can act on.
//
// IP Access Rules need Zone → Firewall Services: Edit, which is NOT among the
// permissions the tunnel and DNS features need — and until 2026-09-01 it was not
// among the ones this panel documented either. So a token created by following
// our own instructions works for domains and silently cannot block anything, and
// the only symptom is a bare authentication error at the moment you try.
//
// It cost a real hour: on this fleet a token replaced on 2026-07-31 broke
// Cloudflare blocking for five weeks. Three of Kvasir's blocks failed against it
// on 4-5 August, and nobody connected the two until an unblock failed in the UI.
func cfFirewallHint(err error) error {
	if err == nil || !strings.Contains(err.Error(), "code 10000") {
		return err
	}
	return fmt.Errorf("%w — the API token is not authorised for firewall rules. "+
		"Cloudflare IP blocking needs Zone → Firewall Services: Edit, which the tunnel "+
		"and DNS permissions do not include; add it to the token in the Cloudflare "+
		"dashboard (Settings → Domains holds the token)", err)
}

// allowlistEntries are addresses or CIDRs the admin has declared never-block, on
// top of the built-in protected ranges. The built-ins only cover what somebody
// thought of in advance; this is where the things only this install knows about
// go — its own monitoring, an office address, a payment provider's webhook source.
//
// Stored as one entry per line (or comma-separated) in the block_allowlist setting.
// A bare address is matched exactly; a CIDR matches its range.
func (s *Server) allowlistEntries(ctx context.Context) []string {
	raw := s.getSetting(ctx, "block_allowlist")
	out := []string{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == ',' || r == ' ' || r == '\t' || r == '\r'
	}) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// allowlisted returns the entry that protects ip, or "" if none does.
func (s *Server) allowlisted(ctx context.Context, ip net.IP) string {
	for _, entry := range s.allowlistEntries(ctx) {
		if strings.Contains(entry, "/") {
			if _, n, err := net.ParseCIDR(entry); err == nil && n.Contains(ip) {
				return entry
			}
			continue
		}
		if p := net.ParseIP(entry); p != nil && p.Equal(ip) {
			return entry
		}
	}
	return ""
}

// vetBlock runs every guard that can refuse an address, in one place, so the
// propose path and the enforce path can never disagree about what is blockable.
// Returns the normalized address, or a blockRefusal naming which guard objected.
func (s *Server) vetBlock(ctx context.Context, ip string) (string, error) {
	clean, err := checkBlockable(ip)
	if err != nil {
		return "", err
	}
	parsed := net.ParseIP(clean)
	if entry := s.allowlisted(ctx, parsed); entry != "" {
		return "", refuse("refusing to block %s — it is on this panel's allowlist (%s)", clean, entry)
	}
	if s.isOperatorIP(ctx, clean) {
		return "", refuse("refusing to block %s — an administrator signed in to this panel from that address recently; blocking it would lock you out of your own site", clean)
	}
	return clean, nil
}

// cfFirewallClient builds a Cloudflare client for edge-firewall calls. Unlike
// cfClient it needs only the API token (no tunnel/account), since IP Access Rules
// are zone-scoped. Returns nil when Cloudflare isn't enabled/configured.
func (s *Server) cfFirewallClient(ctx context.Context) (*cloudflare.Client, error) {
	if s.getSetting(ctx, "cf_enabled") != "1" {
		return nil, nil
	}
	encToken := s.getSetting(ctx, "cf_api_token")
	if encToken == "" {
		return nil, nil
	}
	token, err := s.cipher.Decrypt(encToken)
	if err != nil {
		return nil, fmt.Errorf("decrypt Cloudflare token: %w", err)
	}
	return cloudflare.New(token, s.getSetting(ctx, "cf_account_id"), "", ""), nil
}

// serverBlockHost returns the public hostname associated with a server, so we can
// resolve which Cloudflare zone (if any) owns it. Prefers the Cloudflare-Tunnel
// hostname, then the NPM subdomain resolved against the base domain.
func (s *Server) serverBlockHost(serverID string) string {
	if serverID == "" {
		return ""
	}
	var cf, sub string
	s.db.QueryRow("SELECT COALESCE(cf_hostname,''), COALESCE(subdomain,'') FROM servers WHERE id=?", serverID).Scan(&cf, &sub)
	if strings.TrimSpace(cf) != "" {
		return strings.TrimSpace(cf)
	}
	return s.cfFullDomain(context.Background(), sub)
}

type blockedIP struct {
	ID        string `json:"id"`
	IP        string `json:"ip"`
	Backend   string `json:"backend"` // cloudflare | nftables
	Scope     string `json:"scope"`   // CF zone id (cloudflare) or '' (host-wide)
	ServerID  string `json:"server_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Source    string `json:"source"` // manual | kvasir
	CreatedAt string `json:"created_at,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// kvasirBlockTTL is how long one of Kvasir's own blocks lasts before it lifts
// itself. Manual blocks never expire — a human who typed an address in has said
// what they want.
//
// This is what makes auto mode defensible at all. Every guard in this file only
// covers a mistake somebody anticipated; an expiry covers the ones nobody did. A
// scanner that is still scanning gets blocked again within the hour, while a
// wrong block stops being wrong overnight instead of sitting there until a human
// happens to look at the list.
const kvasirBlockTTL = 48 * time.Hour

// blockIP enforces a block for ip, choosing Cloudflare when a zone the token can see
// owns host, else the host firewall (if enabled). It records the block and returns a
// short human description of what was done. serverID/host may be empty for a manual
// host-firewall block. reason/source are stored for the audit trail.
func (s *Server) blockIP(ctx context.Context, serverID, host, ip, reason, source string) (blockedIP, error) {
	clean, err := s.vetBlock(ctx, ip)
	if err != nil {
		return blockedIP{}, err
	}
	if s.getSetting(ctx, "block_enabled") != "1" {
		return blockedIP{}, fmt.Errorf("IP blocking is disabled — enable it in Settings → Security")
	}
	if source == "" {
		source = "manual"
	}
	notes := fmt.Sprintf("Yggdrasil block (%s): %s", source, reason)

	// Prefer Cloudflare when the panel's token controls the zone for this host.
	if host != "" {
		if client, cerr := s.cfFirewallClient(ctx); cerr == nil && client != nil {
			if zone, zerr := client.ZoneForHost(host); zerr == nil && zone != "" {
				ruleID, berr := client.BlockIP(zone, clean, notes)
				berr = cfFirewallHint(berr)
				if berr != nil {
					return blockedIP{}, fmt.Errorf("cloudflare block failed: %w", berr)
				}
				return s.recordBlock(ctx, serverID, clean, "cloudflare", zone, ruleID, reason, source)
			}
		}
	}

	// Fall back to the host firewall (nftables) for directly-exposed sites.
	if s.getSetting(ctx, "block_nft_enabled") != "1" {
		if host != "" {
			return blockedIP{}, fmt.Errorf("no Cloudflare zone the panel controls owns %q, and host-firewall blocking is off — enable it in Settings → Security to block at the host", host)
		}
		return blockedIP{}, fmt.Errorf("host-firewall blocking is off — enable it in Settings → Security")
	}
	if err := firewall.Block(clean); err != nil {
		return blockedIP{}, err
	}
	return s.recordBlock(ctx, serverID, clean, "nftables", "", "", reason, source)
}

// recordBlock upserts the blocked_ips row and audits + notifies. A duplicate
// (same ip/backend/scope) updates the existing row rather than erroring.
func (s *Server) recordBlock(ctx context.Context, serverID, ip, backend, scope, cfRuleID, reason, source string) (blockedIP, error) {
	expires := ""
	if source == "kvasir" {
		expires = time.Now().Add(kvasirBlockTTL).UTC().Format("2006-01-02 15:04:05")
	}
	b := blockedIP{
		ID: uuid.New().String(), IP: ip, Backend: backend, Scope: scope,
		ServerID: serverID, Reason: reason, Source: source, ExpiresAt: expires,
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO blocked_ips (id, ip, backend, scope, cf_rule_id, server_id, reason, source, expires_at)
		 VALUES (?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(ip, backend, scope) DO UPDATE SET
		    cf_rule_id=excluded.cf_rule_id, server_id=excluded.server_id,
		    reason=excluded.reason, source=excluded.source, expires_at=excluded.expires_at`,
		b.ID, ip, backend, scope, cfRuleID, serverID, reason, source, expires)
	if err != nil {
		return blockedIP{}, fmt.Errorf("record block: %w", err)
	}
	// On a re-block (ON CONFLICT UPDATE) the row keeps its original id, so read the
	// authoritative id back rather than returning the freshly-generated one.
	s.db.QueryRowContext(ctx, "SELECT id FROM blocked_ips WHERE ip=? AND backend=? AND scope=?", ip, backend, scope).Scan(&b.ID)
	where := "at Cloudflare"
	if backend == "nftables" {
		where = "in the host firewall"
	}
	s.auditSystem("block.create", "ip:"+ip, source, map[string]any{"backend": backend, "scope": scope, "reason": reason})
	if serverID != "" {
		s.notifyServer(serverID, fmt.Sprintf("🛡 Blocked **%s** %s — %s", ip, where, reason))
	} else {
		s.notifyAll(fmt.Sprintf("🛡 Blocked **%s** %s — %s", ip, where, reason))
	}
	return b, nil
}

// unblockByID removes a recorded block from its backend and deletes the row.
func (s *Server) unblockByID(ctx context.Context, id string) error {
	var ip, backend, scope, cfRuleID string
	err := s.db.QueryRowContext(ctx,
		"SELECT ip, backend, scope, COALESCE(cf_rule_id,'') FROM blocked_ips WHERE id=?", id).
		Scan(&ip, &backend, &scope, &cfRuleID)
	if err != nil {
		return fmt.Errorf("no such block")
	}
	switch backend {
	case "cloudflare":
		client, cerr := s.cfFirewallClient(ctx)
		if cerr != nil {
			return cerr
		}
		if client == nil {
			return fmt.Errorf("cloudflare is no longer configured — cannot remove the edge rule")
		}
		if err := cfFirewallHint(client.UnblockIP(scope, cfRuleID)); err != nil {
			return err
		}
	case "nftables":
		if err := firewall.Unblock(ip); err != nil {
			return err
		}
	}
	s.db.ExecContext(ctx, "DELETE FROM blocked_ips WHERE id=?", id)
	s.auditSystem("block.delete", "ip:"+ip, "admin", map[string]any{"backend": backend})
	return nil
}

// kvasirApplyBlock is the Kvasir "active-help" path for a block_ip suggestion. It
// only enforces when block_mode is "auto"; otherwise it leaves a proposal for the
// admin. Rate-limited like other auto-actions.
func (s *Server) kvasirApplyBlock(serverID string, dec kvasirDecision, body string) (bool, string) {
	ctx := context.Background()
	ip := strings.TrimSpace(dec.Args)
	// Vet before proposing, not just before enforcing: an address a guard would
	// refuse is a suggestion that was WRONG, and recording it as "proposed" — the
	// same word used for the eight good ones — is how a proposal to block
	// Cloudflare's own edge went unnoticed for a month.
	if _, err := s.vetBlock(ctx, ip); err != nil {
		s.notifyServer(serverID, body+"\n\n_I can't block that address: "+err.Error()+"._")
		return false, "refused"
	}
	proposeNote := body + "\n\n_I'm leaving this block for you to apply from Settings → Security (blocking is in propose mode)._"
	if s.getSetting(ctx, "block_enabled") != "1" || s.getSetting(ctx, "block_mode") != "auto" {
		s.notifyServer(serverID, proposeNote)
		return false, "proposed"
	}
	if !s.kvasir.allowAction(serverID, time.Now()) {
		s.notifyServer(serverID, body+kvasirRateLimitNote)
		return false, "rate-limited"
	}
	host := s.serverBlockHost(serverID)
	if _, err := s.blockIP(ctx, serverID, host, ip, "auto-block: "+dec.Reason, "kvasir"); err != nil {
		if isRefusal(err) {
			s.notifyServer(serverID, body+"\n\n_I can't block that address: "+err.Error()+"._")
			return false, "refused"
		}
		s.notifyServer(serverID, body+"\n\n⚠️ I tried to block "+ip+" but it failed: "+err.Error())
		return false, "block failed"
	}
	s.notifyServer(serverID, body+fmt.Sprintf("\n\n✅ I blocked **%s**. Remove it from Settings → Security if it was wrong.", ip))
	return true, "blocked"
}

// ---- HTTP (admin only) ----

func (s *Server) handleListBlocks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, ip, backend, scope, COALESCE(server_id,''), COALESCE(reason,''), source, created_at, COALESCE(expires_at,'') FROM blocked_ips ORDER BY created_at DESC")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := []blockedIP{}
	for rows.Next() {
		var b blockedIP
		if rows.Scan(&b.ID, &b.IP, &b.Backend, &b.Scope, &b.ServerID, &b.Reason, &b.Source, &b.CreatedAt, &b.ExpiresAt) == nil {
			list = append(list, b)
		}
	}
	jsonOK(w, list)
}

func (s *Server) handleCreateBlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP       string `json:"ip"`
		Host     string `json:"host"`
		ServerID string `json:"server_id"`
		Reason   string `json:"reason"`
	}
	if decodeJSON(r, &req) != nil || strings.TrimSpace(req.IP) == "" {
		jsonError(w, "ip is required", http.StatusBadRequest)
		return
	}
	host := strings.TrimSpace(req.Host)
	if host == "" && req.ServerID != "" {
		host = s.serverBlockHost(req.ServerID)
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "manual block"
	}
	b, err := s.blockIP(r.Context(), strings.TrimSpace(req.ServerID), host, req.IP, reason, "manual")
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.auditLog(r, "block.create", "ip:"+b.IP, map[string]any{"backend": b.Backend})
	jsonOK(w, b)
}

func (s *Server) handleDeleteBlock(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.unblockByID(r.Context(), id); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.auditLog(r, "block.delete", "block:"+id, nil)
	jsonOK(w, map[string]string{"status": "unblocked"})
}

// blockMode returns the configured Kvasir block mode, defaulting to "propose".
func (s *Server) blockMode(ctx context.Context) string {
	switch s.getSetting(ctx, "block_mode") {
	case "off":
		return "off"
	case "auto":
		return "auto"
	default:
		return "propose"
	}
}

func (s *Server) handleGetBlockSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	jsonOK(w, map[string]any{
		"enabled":       s.getSetting(ctx, "block_enabled") == "1",
		"mode":          s.blockMode(ctx),
		"nft_enabled":   s.getSetting(ctx, "block_nft_enabled") == "1",
		"nft_available": firewall.Available(),
		"cf_configured": s.getSetting(ctx, "cf_enabled") == "1" && s.getSetting(ctx, "cf_api_token") != "",
		"allowlist":     strings.Join(s.allowlistEntries(ctx), "\n"),
	})
}

func (s *Server) handleSetBlockSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled    bool   `json:"enabled"`
		Mode       string `json:"mode"`
		NftEnabled bool   `json:"nft_enabled"`
		Allowlist  string `json:"allowlist"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode != "off" && mode != "propose" && mode != "auto" {
		mode = "propose"
	}
	ctx := r.Context()
	// If enabling host-firewall blocking, make sure the nftables scaffold exists now
	// so the admin gets an immediate, clear error (missing privileges/binary) BEFORE
	// we persist a setting that wouldn't actually work.
	if req.NftEnabled {
		if err := firewall.Ensure(); err != nil {
			jsonError(w, "host-firewall blocking needs setup: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	// Validate the allowlist before saving: an entry with a typo protects nothing,
	// and it would fail silently at the only moment it mattered.
	clean := []string{}
	for _, e := range strings.FieldsFunc(req.Allowlist, func(r rune) bool {
		return r == '\n' || r == ',' || r == ' ' || r == '\t' || r == '\r'
	}) {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			if _, _, err := net.ParseCIDR(e); err != nil {
				jsonError(w, "allowlist: "+e+" is not a valid CIDR", http.StatusBadRequest)
				return
			}
		} else if net.ParseIP(e) == nil {
			jsonError(w, "allowlist: "+e+" is not a valid IP address", http.StatusBadRequest)
			return
		}
		clean = append(clean, e)
	}
	s.setSetting(ctx, "block_allowlist", strings.Join(clean, "\n"))
	s.setSetting(ctx, "block_enabled", boolStr(req.Enabled))
	s.setSetting(ctx, "block_mode", mode)
	s.setSetting(ctx, "block_nft_enabled", boolStr(req.NftEnabled))
	s.auditLog(r, "settings.blocking", "blocking", map[string]any{"enabled": req.Enabled, "mode": mode, "nft": req.NftEnabled, "allowlist": len(clean)})
	jsonOK(w, map[string]any{"enabled": req.Enabled, "mode": mode, "nft_enabled": req.NftEnabled, "allowlist": strings.Join(clean, "\n")})
}

// ---- expiry ----

// blockExpiryScan is how often expired auto-blocks are swept. Well under the TTL,
// so "48 hours" means roughly that rather than "48 hours plus however long until
// the next sweep".
const blockExpiryScan = 10 * time.Minute

// startBlockExpiryLoop lifts Kvasir's blocks when their time is up.
//
// Without this the expiry column would be decoration, and decoration is worse
// than nothing here: it would read like a safety net on the settings page while
// leaving every wrong block in place forever.
func (s *Server) startBlockExpiryLoop() {
	go func() {
		defer recoverLog("blockExpiryLoop")
		// A first pass shortly after boot, so blocks that expired while the panel
		// was down do not wait out another interval.
		time.Sleep(90 * time.Second)
		s.expireBlocks()
		t := time.NewTicker(blockExpiryScan)
		defer t.Stop()
		for range t.C {
			s.expireBlocks()
		}
	}()
}

// dueBlock is a block whose time is up.
type dueBlock struct{ id, ip string }

// blocksDue selects the automatic blocks that have outlived their TTL. Split out
// from expireBlocks so the selection — the part with the date arithmetic and the
// "manual blocks never expire" rule in it — is testable without a live firewall.
func (s *Server) blocksDue(ctx context.Context, now time.Time) []dueBlock {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ip FROM blocked_ips
		  WHERE COALESCE(expires_at,'') <> '' AND expires_at <= ?`,
		now.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []dueBlock
	for rows.Next() {
		var d dueBlock
		if rows.Scan(&d.id, &d.ip) == nil {
			list = append(list, d)
		}
	}
	return list
}

func (s *Server) expireBlocks() {
	defer recoverLog("expireBlocks")
	ctx := context.Background()
	for _, d := range s.blocksDue(ctx, time.Now()) {
		if err := s.unblockByID(ctx, d.id); err != nil {
			log.Printf("block expiry: could not lift %s: %v", d.ip, err)
			continue
		}
		s.notifyAll(fmt.Sprintf("⌛ Lifted the automatic block on **%s** — it had run its 48 hours. If it comes back, it gets blocked again.", d.ip))
	}
}
