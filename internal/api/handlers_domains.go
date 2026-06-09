package api

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// Domains overview — phase 2 of the NPM / Cloudflare Tunnel subdomain
// integrations: one flat list of the domains the panel routes (one entry per
// server × provider), plus an on-demand "does the public URL answer?" probe.
// The probe derives the domain server-side from the stored subdomain, so the
// endpoint can't be pointed at arbitrary hosts.

type domainEntry struct {
	ServerID    string `json:"server_id"`
	ServerName  string `json:"server_name"`
	GameskillID string `json:"gameskill_id"`
	Status      string `json:"status"`
	Subdomain   string `json:"subdomain"`
	Provider    string `json:"provider"` // "npm" | "cloudflare"
	Domain      string `json:"domain"`
	Provisioned bool   `json:"provisioned"` // a proxy host / ingress rule is recorded
	Port        int    `json:"port"`        // internal web port the domain routes to (0 = none)
}

func (s *Server) handleListDomains(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, name, gameskill_id, COALESCE(realm_id,''), status,
		       COALESCE(subdomain,''), COALESCE(cf_hostname,''), COALESCE(npm_host_id,0)
		FROM servers ORDER BY name`)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	// Scan everything and CLOSE the cursor before any further queries (modernc
	// SQLite single-connection deadlock — same caveat as handleListServers).
	type domRow struct {
		id, name, gsID, realmID, status, subdomain, cfHostname string
		npmHostID                                              int
	}
	var all []domRow
	for rows.Next() {
		var d domRow
		if err := rows.Scan(&d.id, &d.name, &d.gsID, &d.realmID, &d.status,
			&d.subdomain, &d.cfHostname, &d.npmHostID); err != nil {
			continue
		}
		all = append(all, d)
	}
	rows.Close()

	admin := isAdmin(r)
	var grants []rbac.Grant
	if !admin {
		if c := claimsFromContext(r.Context()); c != nil {
			grants = s.loadGrants(r.Context(), c.UserID)
		}
	}

	npmOn := s.getSetting(r.Context(), "npm_enabled") == "1"
	cfOn := s.getSetting(r.Context(), "cf_enabled") == "1"

	list := []domainEntry{}
	for _, d := range all {
		sub := normalizeSubdomain(d.subdomain)
		if sub == "" && d.cfHostname == "" {
			continue
		}
		if !admin && !rbac.VisibleServer(grants, rbac.Target{ServerID: d.id, RealmID: d.realmID, GameskillID: d.gsID}) {
			continue
		}
		port := s.serverWebPort(r.Context(), d.id)
		base := domainEntry{
			ServerID: d.id, ServerName: d.name, GameskillID: d.gsID,
			Status: d.status, Subdomain: sub, Port: port,
		}
		if npmOn && sub != "" {
			if domain := s.npmFullDomain(r.Context(), sub); domain != "" {
				e := base
				e.Provider, e.Domain, e.Provisioned = "npm", domain, d.npmHostID != 0
				list = append(list, e)
			}
		}
		// Cloudflare: prefer the recorded hostname (it's what is actually
		// provisioned, robust to base-domain edits); show it even when the
		// integration was later disabled so orphans stay visible.
		switch {
		case d.cfHostname != "":
			e := base
			e.Provider, e.Domain, e.Provisioned = "cloudflare", d.cfHostname, true
			list = append(list, e)
		case cfOn && sub != "":
			if domain := s.cfFullDomain(r.Context(), sub); domain != "" {
				e := base
				e.Provider, e.Domain, e.Provisioned = "cloudflare", domain, false
				list = append(list, e)
			}
		}
	}
	jsonOK(w, list)
}

// --- Reachability probe ---

type domainCheckEntry struct {
	at   time.Time
	data map[string]any
}

var (
	domainCheckMu    sync.Mutex
	domainCheckCache = map[string]domainCheckEntry{}
)

// handleCheckDomain probes the public URL of a server's domain for one
// provider. The domain is recomputed from the stored subdomain/settings —
// never taken from the request — so this can't be used to probe arbitrary
// hosts. Returns {reachable, status, url}.
func (s *Server) handleCheckDomain(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerView, s.serverTarget(r.Context(), id)) {
		return
	}
	provider := r.URL.Query().Get("provider")
	domain := s.domainForServer(r.Context(), id, provider)
	if domain == "" {
		jsonError(w, "no domain configured for this server/provider", http.StatusNotFound)
		return
	}

	// Domain is part of the key so a subdomain change doesn't serve a stale probe.
	key := id + "|" + provider + "|" + domain
	domainCheckMu.Lock()
	if e, ok := domainCheckCache[key]; ok && time.Since(e.at) < 30*time.Second {
		domainCheckMu.Unlock()
		jsonOK(w, e.data)
		return
	}
	domainCheckMu.Unlock()

	out := probeDomain(domain)

	domainCheckMu.Lock()
	domainCheckCache[key] = domainCheckEntry{at: time.Now(), data: out}
	domainCheckMu.Unlock()
	jsonOK(w, out)
}

// domainForServer resolves the domain the panel manages for a server under the
// given provider ("npm" or "cloudflare"), or "" when none applies.
func (s *Server) domainForServer(ctx context.Context, serverID, provider string) string {
	var sub, cfHostname string
	s.db.QueryRowContext(ctx,
		"SELECT COALESCE(subdomain,''), COALESCE(cf_hostname,'') FROM servers WHERE id=?",
		serverID).Scan(&sub, &cfHostname)
	switch provider {
	case "npm":
		return s.npmFullDomain(ctx, sub)
	case "cloudflare":
		if cfHostname != "" {
			return cfHostname
		}
		return s.cfFullDomain(ctx, sub)
	}
	return ""
}

// probeDomain fetches https://domain/ (falling back to http://) and reports
// whether anything answered, with the HTTP status. Redirects are not followed
// — a 301/302 from the proxy already proves the route works.
func probeDomain(domain string) map[string]any {
	client := &http.Client{
		Timeout:       6 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for _, scheme := range []string{"https", "http"} {
		url := scheme + "://" + domain + "/"
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			// Self-signed cert (e.g. an app serving its own HTTPS behind no proxy
			// cert): the route still works, so retry once without verification.
			if scheme == "https" && strings.Contains(err.Error(), "certificate") {
				insecure := &http.Client{
					Timeout:       6 * time.Second,
					CheckRedirect: client.CheckRedirect,
					Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
				}
				if resp2, err2 := insecure.Do(req); err2 == nil {
					resp2.Body.Close()
					return map[string]any{"reachable": true, "status": resp2.StatusCode, "url": url, "self_signed": true}
				}
			}
			continue
		}
		resp.Body.Close()
		return map[string]any{"reachable": true, "status": resp.StatusCode, "url": url}
	}
	return map[string]any{"reachable": false, "status": 0, "url": "https://" + domain + "/"}
}
