package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Panel-to-panel transfer, pull-style: this panel fetches a server bundle
// straight from another panel's export endpoint and imports the stream.
//
// Why a pull and not an upload. Exports stream, so a multi-GB download is fine
// through a tunnel — but an upload is a request *body*, and Cloudflare caps
// those at 100 MB (Free/Pro). A browser-mediated push of a real server data dir
// therefore cannot work behind a tunnel no matter what the panel does. Inverting
// the direction turns the transfer into a large *response*, which nothing caps,
// and takes the browser out of the data path entirely.
//
// The source address is whatever the admin types, so two panels on the same
// tailnet can pull over it directly (http://100.x.x.x:8080) and skip the CDN
// altogether.
//
// SECURITY: this makes the panel fetch a URL an operator supplies — server-side
// request forgery by construction. It cannot be allowlisted the way the GitHub
// fetcher is, because reaching private/tailnet addresses is the entire point. It
// is therefore admin-only and audited, and the response is only ever consumed as
// a server bundle. The bundle carries DECRYPTED secrets, so a pull should run
// over HTTPS or a private network, never plain HTTP across the internet.

// remotePanel holds the address + credential for one source panel.
type remotePanel struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// normalizeRemote validates the address and returns it without a trailing slash.
func normalizeRemote(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("the source panel's address is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw // a bare host:port is the common tailnet case
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("not a usable address: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("address must be http or https")
	}
	if u.Host == "" {
		return "", fmt.Errorf("address is missing a host")
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}

// remoteHTTP is used for both the listing and the (potentially hours-long)
// bundle stream. There is deliberately no overall Timeout — a multi-GB transfer
// would trip it — but the connection and response-header phases are bounded, so
// an unreachable host still fails promptly instead of hanging the request.
var remoteHTTP = &http.Client{
	Transport: &http.Transport{
		ResponseHeaderTimeout: 30 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
	},
}

// remoteGet performs an authenticated GET against the source panel.
func remoteGet(base, token, path string) (*http.Response, error) {
	req, err := http.NewRequest("GET", base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("User-Agent", "yggdrasil-panel-transfer")
	resp, err := remoteHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the source panel: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		resp.Body.Close()
		return nil, fmt.Errorf("the source panel rejected the token (HTTP %d) — create an API token there and paste it here", resp.StatusCode)
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, fmt.Errorf("not found on the source panel (HTTP 404) — check the address and that it runs a recent Yggdrasil")
	default:
		resp.Body.Close()
		return nil, fmt.Errorf("the source panel returned HTTP %d", resp.StatusCode)
	}
}

// handleRemoteServers lists the servers on a source panel so the admin can pick
// one. Nothing is stored: the address and token live only in this request.
func (s *Server) handleRemoteServers(w http.ResponseWriter, r *http.Request) {
	var req remotePanel
	if decodeJSON(r, &req) != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	base, err := normalizeRemote(req.URL)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		jsonError(w, "an API token from the source panel is required", http.StatusBadRequest)
		return
	}
	resp, err := remoteGet(base, req.Token, "/api/servers")
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var remote []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		GameskillID string `json:"gameskill_id"`
		Status      string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&remote); err != nil {
		jsonError(w, "the source panel's reply wasn't a server list — is that address an Yggdrasil panel?", http.StatusBadGateway)
		return
	}

	// Flag names that already exist here, so the admin sees the clash before
	// starting a long transfer rather than after it.
	here := map[string]bool{}
	if rows, qerr := s.db.QueryContext(r.Context(), "SELECT name FROM servers"); qerr == nil {
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				here[strings.ToLower(n)] = true
			}
		}
		rows.Close()
	}
	out := make([]map[string]any, 0, len(remote))
	for _, rs := range remote {
		out = append(out, map[string]any{
			"id": rs.ID, "name": rs.Name, "gameskill_id": rs.GameskillID,
			"status": rs.Status, "exists_here": here[strings.ToLower(rs.Name)],
		})
	}
	s.auditLog(r, "panel.remote_list", "panel:"+base, map[string]any{"servers": len(out)})
	jsonOK(w, out)
}

// handleRemoteImport streams one server's bundle from the source panel straight
// into the normal import path. The bundle never touches disk here as a separate
// file — it is consumed as it arrives.
func (s *Server) handleRemoteImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		remotePanel
		ServerID string `json:"server_id"`
		Skip     bool   `json:"skip_existing"`
	}
	if decodeJSON(r, &req) != nil || strings.TrimSpace(req.ServerID) == "" {
		jsonError(w, "source panel and server_id are required", http.StatusBadRequest)
		return
	}
	base, err := normalizeRemote(req.URL)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := remoteGet(base, req.Token, "/api/servers/"+url.PathEscape(strings.TrimSpace(req.ServerID))+"/export")
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// The import reads the stream as it arrives; nothing is buffered whole.
	out, err := s.importServerBundle(r.Context(), resp.Body, req.Skip)
	if err != nil {
		jsonError(w, "import: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.auditLog(r, "panel.remote_import", "server:"+fmt.Sprint(out["id"]),
		map[string]any{"source": base, "name": out["name"]})
	jsonOK(w, out)
}
