package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/auth"
)

// OAuth 2.1, so this panel can be added to Claude as a **connector**.
//
// A connector is not the same thing as an API token. Claude's own servers make
// the connection, and they will not carry a token you paste into a config file —
// the client registers itself, sends you here in a browser to approve, and
// receives a token bound to this panel. That is four endpoints and a consent
// page, which is what this file is:
//
//	/.well-known/oauth-protected-resource   what this resource is, and who issues tokens for it (RFC 9728)
//	/.well-known/oauth-authorization-server what the authorization server can do (RFC 8414)
//	POST /oauth/register                    dynamic client registration (RFC 7591)
//	GET|POST /oauth/authorize               the consent screen, and the code it issues
//	POST /oauth/token                       code → access token, and refresh
//
// The panel is both the resource server and the authorization server, so there
// is no third party to trust and no secret to configure: your existing panel
// login IS the authorization. A token inherits the permissions of the user who
// approved it, exactly like an API token.
//
// Deliberate choices worth knowing:
//   - **Public clients with PKCE only.** No client secrets are issued. The spec
//     requires PKCE for MCP clients, and a secret adds nothing when the client is
//     an app we have never met.
//   - **Codes and tokens are stored hashed**, never in plaintext, like the panel's
//     API tokens. A leaked database yields no usable credential.
//   - **Access tokens are audience-bound** to this panel's MCP endpoint and are
//     rejected anywhere else, which is what the spec's token-passthrough rules ask
//     for. Refresh tokens rotate on every use, as OAuth 2.1 requires for public
//     clients.
const (
	oauthTokenPrefix   = "ygg_mcp_"
	oauthCodeTTL       = 5 * time.Minute
	oauthAccessTTL     = 30 * 24 * time.Hour // long-lived on purpose: a connector nobody re-approves weekly
	mcpResourcePath    = "/api/mcp"
	oauthMetadataPath  = "/.well-known/oauth-protected-resource"
	oauthAuthorizePath = "/oauth/authorize"
)

// panelName is this panel's display name, for the consent screen and the MCP
// server info. Same setting the sidebar and notifications use.
func (s *Server) panelName() string {
	if n := strings.TrimSpace(s.getSetting(context.Background(), "panel_name")); n != "" {
		return n
	}
	return "Yggdrasil Panel"
}

// mcpResourceURI is the canonical identifier of this MCP server, used as the
// audience of every token. Derived from the request so it matches whatever
// address the panel is actually reached on (NPM, Cloudflare, plain IP).
func mcpResourceURI(r *http.Request) string { return panelBaseURL(r) + mcpResourcePath }

// handleOAuthProtectedResource tells a client which authorization server issues
// tokens for this MCP endpoint — the first thing an MCP client fetches after a
// 401. RFC 9728.
func (s *Server) handleOAuthProtectedResource(w http.ResponseWriter, r *http.Request) {
	base := panelBaseURL(r)
	jsonOK(w, map[string]any{
		"resource":                 mcpResourceURI(r),
		"authorization_servers":    []string{base},
		"scopes_supported":         []string{"yggdrasil"},
		"bearer_methods_supported": []string{"header"},
		"resource_documentation":   base + "/docs/guides-claude-connector.html",
	})
}

// handleOAuthServerMetadata describes this authorization server. RFC 8414.
func (s *Server) handleOAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	base := panelBaseURL(r)
	jsonOK(w, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + oauthAuthorizePath,
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"yggdrasil"},
	})
}

// handleOAuthRegister is dynamic client registration (RFC 7591): a client we have
// never seen asks for a client_id by telling us where to send the user back.
// Open by design — that is what lets Claude connect without anyone pre-arranging
// credentials — and harmless on its own: a client_id grants nothing until a real
// user approves it on the consent screen below.
func (s *Server) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.RedirectURIs) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "redirect_uris is required")
		return
	}
	for _, u := range req.RedirectURIs {
		if err := validateRedirectURI(u); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
	}
	uris, _ := json.Marshal(req.RedirectURIs)
	id := "ygg-client-" + uuid.New().String()
	name := strings.TrimSpace(req.ClientName)
	if name == "" {
		name = "MCP client"
	}
	if _, err := s.db.ExecContext(r.Context(),
		"INSERT INTO oauth_clients (id, name, redirect_uris) VALUES (?,?,?)", id, name, string(uris)); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not register the client")
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]any{
		"client_id":                  id,
		"client_id_issued_at":        time.Now().Unix(),
		"client_name":                name,
		"redirect_uris":              req.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
}

// validateRedirectURI enforces the spec's rule: HTTPS, or localhost for a client
// running on the user's own machine. Anything else is an open-redirect risk.
func validateRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() {
		return fmt.Errorf("redirect_uri must be an absolute URL")
	}
	host := u.Hostname()
	if u.Scheme == "https" || host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	return fmt.Errorf("redirect_uri must use https (or be a localhost address)")
}

// authorizeParams is one in-flight authorization request, carried through the
// consent page in the form rather than in server-side state.
type authorizeParams struct {
	ClientID    string
	RedirectURI string
	State       string
	Challenge   string
	Resource    string
	Scope       string
}

func readAuthorizeParams(v url.Values) authorizeParams {
	return authorizeParams{
		ClientID:    v.Get("client_id"),
		RedirectURI: v.Get("redirect_uri"),
		State:       v.Get("state"),
		Challenge:   v.Get("code_challenge"),
		Resource:    v.Get("resource"),
		Scope:       v.Get("scope"),
	}
}

// handleAuthorize renders the consent screen. It deliberately does NOT require a
// session to render: the panel's login cookie is SameSite=Strict, so it is not
// sent on this cross-site navigation from Claude. Approving posts back to the
// same origin, which IS same-site, and the cookie arrives with it — that is the
// whole reason consent is a POST rather than a redirect.
func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := readAuthorizeParams(q)

	if q.Get("response_type") != "code" {
		oauthRedirectError(w, r, p, "unsupported_response_type", "only response_type=code is supported")
		return
	}
	if q.Get("code_challenge_method") != "S256" || p.Challenge == "" {
		oauthRedirectError(w, r, p, "invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}
	client, err := s.oauthClient(r, p.ClientID)
	if err != nil {
		// No redirect here: an unknown client_id or a redirect_uri it never
		// registered is exactly the case where bouncing the user onward would be
		// the open redirect the spec warns about.
		oauthError(w, http.StatusBadRequest, "invalid_client", "unknown client_id")
		return
	}
	if !client.allows(p.RedirectURI) {
		oauthError(w, http.StatusBadRequest, "invalid_request", "redirect_uri does not match this client's registration")
		return
	}
	renderConsent(w, consentView{Client: client.Name, Params: p, PanelName: s.panelName()})
}

// handleAuthorizeSubmit is the POST from the consent screen — same-site, so the
// panel's session cookie is present and tells us who is approving.
func (s *Server) handleAuthorizeSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "malformed form")
		return
	}
	p := readAuthorizeParams(r.PostForm)
	client, err := s.oauthClient(r, p.ClientID)
	if err != nil || !client.allows(p.RedirectURI) {
		oauthError(w, http.StatusBadRequest, "invalid_client", "unknown client or redirect_uri")
		return
	}

	claims := s.sessionUser(r)
	if claims == nil {
		// Not logged in on this browser. Re-render the same consent screen with a
		// prompt rather than losing the request — the user opens the panel, signs
		// in, and presses Allow again.
		renderConsent(w, consentView{Client: client.Name, Params: p, PanelName: s.panelName(), NeedLogin: true})
		return
	}
	if r.PostForm.Get("decision") != "allow" {
		oauthRedirectError(w, r, p, "access_denied", "the user declined")
		return
	}

	code, hash, err := auth.GenerateResetToken()
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not issue a code")
		return
	}
	resource := p.Resource
	if resource == "" {
		resource = mcpResourceURI(r)
	}
	if _, err := s.db.ExecContext(r.Context(),
		`INSERT INTO oauth_codes (code_hash, client_id, user_id, redirect_uri, challenge, resource, expires_at)
		 VALUES (?,?,?,?,?,?,?)`,
		hash, p.ClientID, claims.UserID, p.RedirectURI, p.Challenge, resource,
		time.Now().Add(oauthCodeTTL).UTC().Format(time.RFC3339)); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not store the code")
		return
	}
	s.auditLog(r, "oauth.authorize", "client:"+p.ClientID, map[string]any{"client": client.Name})

	u, _ := url.Parse(p.RedirectURI)
	qs := u.Query()
	qs.Set("code", code)
	if p.State != "" {
		qs.Set("state", p.State)
	}
	u.RawQuery = qs.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// handleToken exchanges a code for an access token, or refreshes one.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "malformed form")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.tokenFromCode(w, r)
	case "refresh_token":
		s.tokenFromRefresh(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

func (s *Server) tokenFromCode(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	hash := auth.HashToken(f.Get("code"))
	var clientID, userID, redirectURI, challenge, resource, expires string
	err := s.db.QueryRowContext(r.Context(),
		`SELECT client_id, user_id, redirect_uri, challenge, resource, expires_at FROM oauth_codes WHERE code_hash=?`, hash).
		Scan(&clientID, &userID, &redirectURI, &challenge, &resource, &expires)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown or already-used code")
		return
	}
	// One use, whatever happens next.
	s.db.ExecContext(r.Context(), "DELETE FROM oauth_codes WHERE code_hash=?", hash) //nolint:errcheck

	if t, perr := time.Parse(time.RFC3339, expires); perr != nil || time.Now().After(t) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code has expired — start the connection again")
		return
	}
	if f.Get("client_id") != clientID || f.Get("redirect_uri") != redirectURI {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "client_id or redirect_uri does not match the code")
		return
	}
	// PKCE: the verifier must hash to the challenge recorded at authorize time.
	sum := sha256.Sum256([]byte(f.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	s.issueTokens(w, r, clientID, userID, resource)
}

func (s *Server) tokenFromRefresh(w http.ResponseWriter, r *http.Request) {
	hash := auth.HashToken(r.PostForm.Get("refresh_token"))
	var clientID, userID, resource string
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT client_id, user_id, resource FROM oauth_tokens WHERE refresh_hash=? AND refresh_hash<>''", hash).
		Scan(&clientID, &userID, &resource); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown refresh token")
		return
	}
	// Rotation: the old pair dies here, so a stolen refresh token is single-use
	// and its reuse is visible as a failure rather than silent access.
	s.db.ExecContext(r.Context(), "DELETE FROM oauth_tokens WHERE refresh_hash=?", hash) //nolint:errcheck
	s.issueTokens(w, r, clientID, userID, resource)
}

func (s *Server) issueTokens(w http.ResponseWriter, r *http.Request, clientID, userID, resource string) {
	access, accessHash, err := oauthToken()
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not issue a token")
		return
	}
	refresh, refreshHash, err := oauthToken()
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not issue a token")
		return
	}
	expiry := time.Now().Add(oauthAccessTTL)
	if _, err := s.db.ExecContext(r.Context(),
		`INSERT INTO oauth_tokens (token_hash, refresh_hash, client_id, user_id, resource, expires_at)
		 VALUES (?,?,?,?,?,?)`,
		accessHash, refreshHash, clientID, userID, resource, expiry.UTC().Format(time.RFC3339)); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not store the token")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOK(w, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(oauthAccessTTL.Seconds()),
		"refresh_token": refresh,
		"scope":         "yggdrasil",
	})
}

// oauthToken mints one opaque credential. GenerateResetToken also returns a hash,
// but of the BARE value — this prefixes it first, so the stored hash has to be
// recomputed over the string the client will actually send back.
func oauthToken() (token, hash string, err error) {
	raw, _, err := auth.GenerateResetToken()
	if err != nil {
		return "", "", err
	}
	token = oauthTokenPrefix + raw
	return token, auth.HashToken(token), nil
}

// claimsForOAuthToken resolves an MCP access token to its owner, enforcing both
// expiry and audience: a token minted for one panel address is not accepted on
// another, which is the audience binding the spec requires.
func (s *Server) claimsForOAuthToken(r *http.Request, token string) *auth.Claims {
	var userID, username, role, resource, expires string
	err := s.db.QueryRowContext(r.Context(), `
		SELECT u.id, u.username, u.role, t.resource, t.expires_at
		FROM oauth_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash=? AND u.disabled=0`, auth.HashToken(token)).
		Scan(&userID, &username, &role, &resource, &expires)
	if err != nil {
		return nil
	}
	if t, perr := time.Parse(time.RFC3339, expires); perr != nil || time.Now().After(t) {
		return nil
	}
	if resource != "" && resource != mcpResourceURI(r) {
		return nil
	}
	return &auth.Claims{UserID: userID, Username: username, Role: role}
}

// sessionUser reports who is logged in on this browser, or nil. Cookie only:
// the consent POST is a browser form, and accepting a bearer token here would
// let a stolen API token approve a connector without anyone seeing the screen.
func (s *Server) sessionUser(r *http.Request) *auth.Claims {
	c, err := r.Cookie("ygg_token")
	if err != nil || c.Value == "" {
		return nil
	}
	claims, err := auth.ParseToken(c.Value, s.cfg.Auth.SecretKey)
	if err != nil {
		return nil
	}
	var role string
	var disabled, ver int
	if s.db.QueryRowContext(r.Context(), "SELECT role, disabled, COALESCE(token_version,0) FROM users WHERE id=?", claims.UserID).
		Scan(&role, &disabled, &ver) != nil || disabled == 1 || ver != claims.Ver {
		return nil
	}
	claims.Role = role
	return claims
}

type oauthClientRow struct {
	ID           string
	Name         string
	RedirectURIs []string
}

func (c oauthClientRow) allows(uri string) bool {
	for _, u := range c.RedirectURIs {
		if u == uri {
			return true
		}
	}
	return false
}

func (s *Server) oauthClient(r *http.Request, id string) (oauthClientRow, error) {
	var row oauthClientRow
	var uris string
	if err := s.db.QueryRowContext(r.Context(), "SELECT id, name, redirect_uris FROM oauth_clients WHERE id=?", id).
		Scan(&row.ID, &row.Name, &uris); err != nil {
		return row, err
	}
	json.Unmarshal([]byte(uris), &row.RedirectURIs) //nolint:errcheck
	return row, nil
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
		"error": code, "error_description": desc,
	})
}

// oauthRedirectError reports a failure to the client the OAuth way — back at its
// redirect_uri — falling back to a plain error when there is no usable one.
func oauthRedirectError(w http.ResponseWriter, r *http.Request, p authorizeParams, code, desc string) {
	if p.RedirectURI == "" {
		oauthError(w, http.StatusBadRequest, code, desc)
		return
	}
	u, err := url.Parse(p.RedirectURI)
	if err != nil {
		oauthError(w, http.StatusBadRequest, code, desc)
		return
	}
	q := u.Query()
	q.Set("error", code)
	q.Set("error_description", desc)
	if p.State != "" {
		q.Set("state", p.State)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

type consentView struct {
	Client    string
	PanelName string
	Params    authorizeParams
	NeedLogin bool
}

func renderConsent(w http.ResponseWriter, v consentView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	consentTmpl.Execute(w, v) //nolint:errcheck
}

// The consent screen. Plain server-rendered HTML rather than the SPA: it has to
// work before any JavaScript loads, and it must not depend on the panel's own
// session having been sent — which, being SameSite=Strict, it has not been yet.
var consentTmpl = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connect to {{.PanelName}}</title>
<style>
 :root{--bg:#0b0f14;--panel:#141b24;--border:#243040;--fg:#e6edf3;--muted:#9aa7b4;--accent:#22c55e}
 body{margin:0;background:var(--bg);color:var(--fg);font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;
      display:flex;align-items:center;justify-content:center;min-height:100vh;padding:1.5rem}
 .card{background:var(--panel);border:1px solid var(--border);border-radius:12px;padding:1.75rem;max-width:26rem;width:100%}
 h1{font-size:1.15rem;margin:0 0 .75rem}
 p{color:var(--muted);font-size:.95rem;line-height:1.55;margin:.5rem 0}
 .who{color:var(--fg);font-weight:600}
 ul{color:var(--muted);font-size:.9rem;padding-left:1.1rem;margin:.75rem 0}
 .row{display:flex;gap:.6rem;margin-top:1.25rem}
 button{flex:1;padding:.6rem 1rem;border-radius:8px;border:1px solid var(--border);font-size:.95rem;cursor:pointer}
 .allow{background:var(--accent);color:#04210f;font-weight:600;border-color:transparent}
 .deny{background:transparent;color:var(--muted)}
 .warn{background:#3b2a10;border:1px solid #6b4c15;color:#f5d58a;padding:.7rem .8rem;border-radius:8px;font-size:.9rem}
 a{color:var(--accent)}
</style></head>
<body><div class="card">
<h1>Connect <span class="who">{{.Client}}</span> to {{.PanelName}}?</h1>
{{if .NeedLogin}}
<div class="warn">You are not signed in to this panel in this browser. <a href="/" target="_blank">Open the panel</a>,
sign in, then press Allow again.</div>
{{end}}
<p>It will be able to do what <em>you</em> can do here — read your servers and their logs, and start, stop or
restart them. It gets your permissions, not more.</p>
<ul>
  <li>Access lasts until you revoke it in Settings.</li>
  <li>Every action it takes is recorded in the audit log.</li>
</ul>
<form method="post" action="/oauth/authorize">
  <input type="hidden" name="client_id" value="{{.Params.ClientID}}">
  <input type="hidden" name="redirect_uri" value="{{.Params.RedirectURI}}">
  <input type="hidden" name="state" value="{{.Params.State}}">
  <input type="hidden" name="code_challenge" value="{{.Params.Challenge}}">
  <input type="hidden" name="resource" value="{{.Params.Resource}}">
  <input type="hidden" name="scope" value="{{.Params.Scope}}">
  <div class="row">
    <button class="deny" type="submit" name="decision" value="deny">Cancel</button>
    <button class="allow" type="submit" name="decision" value="allow">Allow</button>
  </div>
</form>
</div></body></html>`))

// --- Managing connections ------------------------------------------------
//
// The consent screen promises the connection can be revoked, so it has to be
// visible somewhere. These back the "Claude connector" card in Settings: what is
// connected, and a way to cut it off. A revoke deletes the tokens, not the
// client registration — the same client can reconnect, but only by asking the
// user to approve again.

func (s *Server) handleListConnections(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT c.id, c.name, MIN(t.created_at), MAX(t.expires_at), COUNT(*)
		FROM oauth_tokens t JOIN oauth_clients c ON c.id = t.client_id
		WHERE t.user_id=? GROUP BY c.id, c.name ORDER BY MIN(t.created_at) DESC`, claims.UserID)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type conn struct {
		ClientID  string `json:"client_id"`
		Name      string `json:"name"`
		Connected string `json:"connected_at"`
		Expires   string `json:"expires_at"`
		Tokens    int    `json:"tokens"`
	}
	list := []conn{}
	for rows.Next() {
		var c conn
		if rows.Scan(&c.ClientID, &c.Name, &c.Connected, &c.Expires, &c.Tokens) == nil {
			list = append(list, c)
		}
	}
	jsonOK(w, list)
}

func (s *Server) handleRevokeConnection(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	clientID := chi.URLParam(r, "id")
	res, err := s.db.ExecContext(r.Context(),
		"DELETE FROM oauth_tokens WHERE client_id=? AND user_id=?", clientID, claims.UserID)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	n, _ := res.RowsAffected()
	s.auditLog(r, "oauth.revoke", "client:"+clientID, map[string]any{"tokens": n})
	jsonOK(w, map[string]any{"revoked": n})
}

// mcpConnectorInfo tells the Settings page what to show: the URL to paste into
// Claude, and whether this panel is reachable the way a connector needs.
func (s *Server) handleMCPInfo(w http.ResponseWriter, r *http.Request) {
	base := panelBaseURL(r)
	jsonOK(w, map[string]any{
		"url":    base + mcpResourcePath,
		"public": strings.HasPrefix(base, "https://") && !isLocalHost(r.Host),
		"tools":  len(mcpTools),
	})
}

// isLocalHost reports whether the panel is being reached on an address only this
// machine or LAN can see — in which case Claude's servers cannot reach it either,
// which is the single most common reason a connector fails to connect.
func isLocalHost(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".lab") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}
