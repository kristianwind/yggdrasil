package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// Moving a website between two panels is two panels' problem, and the half that
// keeps going wrong is DNS.
//
// Each panel provisions a hostname into its OWN tunnel and writes a CNAME to it,
// and EnsureDNS deliberately refuses a hostname whose CNAME points at a different
// tunnel (ErrForeignTunnel) — otherwise any panel could quietly steal a name
// another node is serving. That guard is right, and it is also exactly what makes
// a deliberate move manual: the target cannot claim the name while the source
// still holds it, so somebody has to go into Cloudflare, delete the rule and the
// record, and then remember to start the copy.
//
// Doing it in the wrong order is worse than not doing it, because it half-works:
// the target adds its ingress rule, DNS stays pointed at the source, and the only
// trace is one line in the panel log.
//
// So the move is expressed as what it actually is — a handover. The source
// releases, then the target claims, in that order, as one action.

// handleReleaseDomains drops every hostname this server owns from this panel's
// tunnel/proxy: the ingress rules and the CNAMEs it created. The server itself is
// left alone — still installed, still holding its data, still running if it was.
//
// Not stopping it is deliberate. Releasing is reversible (start the server and it
// re-provisions); stopping a live site because a copy exists somewhere else is
// not the same decision, and it is the operator's to make after they have checked
// the copy actually works.
func (s *Server) handleReleaseDomains(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	// Admin-only: this takes a live site off the internet.
	if !isAdmin(r) {
		jsonError(w, "forbidden: releasing a server's domains takes it off the internet (admin only)", http.StatusForbidden)
		return
	}

	released := s.cfProvisionedHosts(r.Context(), id)
	s.npmRemoveServer(id)
	s.cfRemoveServer(id)

	s.auditLog(r, "server.release_domains", "server:"+id,
		map[string]any{"name": srv.Name, "hostnames": released})
	jsonOK(w, map[string]any{"released": released, "name": srv.Name})
}

// handleTakeoverDomains is the target half: ask the source panel to release the
// hostnames, then provision them here.
//
// The order is the whole point, so it is not optional and not left to the
// operator. The release must be confirmed before anything is claimed — if the
// source cannot be reached, this fails and changes nothing, rather than leaving
// an ingress rule here and DNS still pointed there.
func (s *Server) handleTakeoverDomains(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if !s.can(w, r, rbac.ServerControl, srv.target()) {
		return
	}
	if !isAdmin(r) {
		jsonError(w, "forbidden: taking over a hostname rewrites DNS on both panels (admin only)", http.StatusForbidden)
		return
	}

	var req struct {
		remotePanel
		RemoteID       string `json:"remote_id"`
		SourceServerID string `json:"source_server_id"`
	}
	if decodeJSON(r, &req) != nil || strings.TrimSpace(req.SourceServerID) == "" {
		jsonError(w, "the source panel and its server_id are required", http.StatusBadRequest)
		return
	}
	base, token, err := s.resolveRemote(r.Context(), req.RemoteID, req.URL, req.Token)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Nothing to claim means nothing to release — say so instead of poking the
	// other panel. A server with no hostnames is a normal thing to have.
	if len(s.serverRoutes(r.Context(), id)) == 0 {
		jsonError(w, "this server has no hostnames yet — give it a public hostname first, then take over",
			http.StatusBadRequest)
		return
	}

	// The tunnel rule points at a port on this host, and a stopped server is not
	// listening on it. Handing over to one would take the source's rules away and
	// replace them with rules that answer 502 — the site would go DOWN rather than
	// move, which is the one outcome this endpoint must never produce.
	if srv.Status != "running" {
		jsonError(w, "start this server first — handing its domains over while it is stopped would take the site down, not move it",
			http.StatusConflict)
		return
	}

	released, err := remoteReleaseDomains(base, token, req.SourceServerID)
	if err != nil {
		// Deliberately fatal. Claiming without a release is the half-done state
		// this whole endpoint exists to prevent.
		jsonError(w, "the source panel did not release the hostnames, so nothing was changed here: "+err.Error(),
			http.StatusBadGateway)
		return
	}

	// Provision synchronously: the caller is waiting to be told whether the
	// hostname works now, and the usual fire-and-forget would answer before
	// Cloudflare had been touched.
	s.cfAddServer(id, srv.Name)
	s.npmAddServer(id, srv.Name)

	claimed := s.cfProvisionedHosts(r.Context(), id)
	s.auditLog(r, "server.takeover_domains", "server:"+id,
		map[string]any{"name": srv.Name, "source": base, "released": released, "claimed": claimed})
	jsonOK(w, map[string]any{"released": released, "claimed": claimed, "name": srv.Name})
}

// remoteReleaseDomains calls the source panel's release endpoint and returns the
// hostnames it gave up.
func remoteReleaseDomains(base, token, serverID string) ([]string, error) {
	body, _ := json.Marshal(map[string]any{})
	req, err := http.NewRequest("POST",
		base+"/api/servers/"+url.PathEscape(strings.TrimSpace(serverID))+"/release-domains",
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "yggdrasil-panel-transfer")
	resp, err := remoteHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the source panel: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("the source panel rejected the token (HTTP %d) — it needs an admin token", resp.StatusCode)
	case http.StatusNotFound:
		return nil, fmt.Errorf("the source panel has no server with that id, or runs a version without hostname handover (HTTP 404)")
	default:
		return nil, fmt.Errorf("the source panel returned HTTP %d", resp.StatusCode)
	}
	var out struct {
		Released []string `json:"released"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return nil, fmt.Errorf("the source panel's answer could not be read")
	}
	return out.Released, nil
}
