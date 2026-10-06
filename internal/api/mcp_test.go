package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/auth"
	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/db"
	"time"
)

// mcpTestServer is a panel with a real (in-memory) database and one admin, which
// is all the MCP and OAuth paths touch. No Docker: nothing here starts a
// container.
func mcpTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	s := &Server{db: d, version: "v0.0.0-test",
		cfg: &config.Config{Auth: config.AuthConfig{SecretKey: "test-secret-key", SessionTTL: 24}}}
	if _, err := d.Exec(
		"INSERT INTO users (id, username, password_hash, role) VALUES ('u1','kw','x','admin')"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return s, "u1"
}

// rpc posts one JSON-RPC message as an authenticated caller and returns the
// decoded response.
func rpc(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "https://panel.example/api/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{UserID: "u1", Username: "kw", Role: "admin"}))
	rec := httptest.NewRecorder()
	s.handleMCP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out) //nolint:errcheck
	return rec.Code, out
}

func TestMCPInitializeNegotiatesVersion(t *testing.T) {
	s, _ := mcpTestServer(t)

	_, out := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	result, _ := out["result"].(map[string]any)
	if result == nil {
		t.Fatalf("no result: %v", out)
	}
	if result["protocolVersion"] != "2025-06-18" {
		t.Errorf("should answer in the client's version, got %v", result["protocolVersion"])
	}
	if _, ok := result["capabilities"].(map[string]any)["tools"]; !ok {
		t.Error("must declare the tools capability")
	}

	// A version we do not speak: answer in ours rather than failing, per the
	// lifecycle spec — the client then decides whether to continue.
	_, out = rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	result, _ = out["result"].(map[string]any)
	if result["protocolVersion"] != mcpProtocolVersion {
		t.Errorf("expected fallback to %s, got %v", mcpProtocolVersion, result["protocolVersion"])
	}
}

func TestMCPUnsupportedProtocolHeaderIs400(t *testing.T) {
	s, _ := mcpTestServer(t)
	req := httptest.NewRequest("POST", "https://panel.example/api/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("MCP-Protocol-Version", "1999-01-01")
	rec := httptest.NewRecorder()
	s.handleMCP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an unsupported MCP-Protocol-Version must be 400, got %d", rec.Code)
	}
}

// A notification has no id and therefore no reply — 202 and an empty body. Get
// this wrong and every client logs an error on the initialized notification.
func TestMCPNotificationIsAccepted(t *testing.T) {
	s, _ := mcpTestServer(t)
	req := httptest.NewRequest("POST", "https://panel.example/api/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{UserID: "u1", Role: "admin"}))
	rec := httptest.NewRecorder()
	s.handleMCP(rec, req)
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Errorf("notification: want 202 and empty body, got %d %q", rec.Code, rec.Body.String())
	}
}

func TestMCPToolsListAndUnknownTool(t *testing.T) {
	s, _ := mcpTestServer(t)
	_, out := rpc(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools, _ := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != len(mcpTools) {
		t.Fatalf("expected %d tools, got %d", len(mcpTools), len(tools))
	}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		for _, field := range []string{"name", "description", "inputSchema"} {
			if tool[field] == nil {
				t.Errorf("tool %v is missing %s", tool["name"], field)
			}
		}
	}

	// An unknown tool is a PROTOCOL error, unlike a tool that runs and fails.
	_, out = rpc(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	if out["error"] == nil {
		t.Error("unknown tool should be a JSON-RPC error")
	}

	// An unknown METHOD too.
	_, out = rpc(t, s, `{"jsonrpc":"2.0","id":4,"method":"resources/list"}`)
	if e, _ := out["error"].(map[string]any); e == nil || e["code"].(float64) != -32601 {
		t.Errorf("unknown method should be -32601, got %v", out["error"])
	}
}

// A tool that fails must report it in the RESULT with isError, so the model sees
// the reason and can act on it, rather than as a protocol error it never sees.
func TestMCPToolFailureIsInResult(t *testing.T) {
	s, _ := mcpTestServer(t)
	_, out := rpc(t, s,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"get_server","arguments":{"server":"ghost"}}}`)
	result, _ := out["result"].(map[string]any)
	if result == nil || result["isError"] != true {
		t.Fatalf("expected an isError result, got %v", out)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "ghost") {
		t.Errorf("the message should name what was asked for: %q", text)
	}
}

// Resolving by name is what makes the tools usable from a chat ("restart Bimmelim"),
// and an ambiguous name must refuse rather than pick one.
func TestMCPResolveServerByName(t *testing.T) {
	s, _ := mcpTestServer(t)
	s.db.Exec("INSERT INTO servers (id, name, gameskill_id, data_dir, status) VALUES ('aaaa1111','Bimmelim','mc','/tmp','running')")
	s.db.Exec("INSERT INTO servers (id, name, gameskill_id, data_dir, status) VALUES ('bbbb2222','bimmelim','mc','/tmp','stopped')")
	req := httptest.NewRequest("GET", "/", nil)

	if _, err := s.mcpResolveServer(req, map[string]any{"server": "Bimmelim"}); err != nil {
		t.Errorf("an exact name should win over a case-insensitive twin: %v", err)
	}
	if _, err := s.mcpResolveServer(req, map[string]any{"server": "BIMMELIM"}); err == nil {
		t.Error("a name matching two servers must refuse, not guess")
	}
	if id, err := s.mcpResolveServer(req, map[string]any{"server": "aaaa1111"}); err != nil || id != "aaaa1111" {
		t.Errorf("id lookup failed: %v %q", err, id)
	}
	if _, err := s.mcpResolveServer(req, map[string]any{"server": ""}); err == nil {
		t.Error("an empty name must ask which server")
	}
}

// The whole connector handshake, end to end: register a client, approve it as a
// logged-in user, exchange the code with PKCE, and use the token on /api/mcp.
func TestOAuthConnectorFlow(t *testing.T) {
	s, userID := mcpTestServer(t)

	// 1. Dynamic client registration.
	reg := httptest.NewRequest("POST", "https://panel.example/oauth/register",
		strings.NewReader(`{"redirect_uris":["https://claude.ai/api/mcp/auth_callback"],"client_name":"Claude"}`))
	reg.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleOAuthRegister(rec, reg)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	var client struct {
		ClientID string `json:"client_id"`
	}
	json.Unmarshal(rec.Body.Bytes(), &client) //nolint:errcheck
	if client.ClientID == "" {
		t.Fatal("no client_id issued")
	}

	// 2. Consent screen renders without a session — the panel's cookie is
	//    SameSite=Strict and is not sent on the cross-site arrival from Claude.
	verifier := "a-verifier-long-enough-to-be-realistic-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{
		"response_type": {"code"}, "client_id": {client.ClientID},
		"redirect_uri":   {"https://claude.ai/api/mcp/auth_callback"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
		"state": {"xyz"}, "resource": {"https://panel.example/api/mcp"},
	}
	get := httptest.NewRequest("GET", "https://panel.example/oauth/authorize?"+q.Encode(), nil)
	rec = httptest.NewRecorder()
	s.handleAuthorize(rec, get)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Claude") {
		t.Fatalf("consent screen: %d", rec.Code)
	}
	// A browser carries the page's own nonce back in the form. Read it out of
	// the rendered HTML rather than minting one, so this test exercises the
	// round trip a real client makes.
	csrf := consentCSRFFrom(t, rec.Body.String())

	// 3. Approving posts back same-site, carrying the session cookie.
	session, err := auth.GenerateToken(userID, "kw", "admin", 0, s.cfg.Auth.SecretKey, 24)
	if err != nil {
		t.Fatalf("session token: %v", err)
	}
	form := url.Values{
		"client_id": {client.ClientID}, "redirect_uri": {"https://claude.ai/api/mcp/auth_callback"},
		"code_challenge": {challenge}, "state": {"xyz"}, "decision": {"allow"},
		"resource": {"https://panel.example/api/mcp"}, "csrf": {csrf},
	}
	post := httptest.NewRequest("POST", "https://panel.example/oauth/authorize", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(&http.Cookie{Name: "ygg_token", Value: session})
	rec = httptest.NewRecorder()
	s.handleAuthorizeSubmit(rec, post)
	if rec.Code != http.StatusFound {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	code := loc.Query().Get("code")
	if code == "" || loc.Query().Get("state") != "xyz" {
		t.Fatalf("redirect missing code/state: %s", rec.Header().Get("Location"))
	}

	// 4. Token exchange, with the PKCE verifier.
	tokenForm := url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"client_id": {client.ClientID}, "redirect_uri": {"https://claude.ai/api/mcp/auth_callback"},
		"code_verifier": {verifier},
	}
	tok := httptest.NewRequest("POST", "https://panel.example/oauth/token", strings.NewReader(tokenForm.Encode()))
	tok.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.handleToken(rec, tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("token: %d %s", rec.Code, rec.Body.String())
	}
	var issued struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
	}
	json.Unmarshal(rec.Body.Bytes(), &issued) //nolint:errcheck
	if !strings.HasPrefix(issued.AccessToken, oauthTokenPrefix) || issued.TokenType != "Bearer" {
		t.Fatalf("unexpected token: %+v", issued)
	}

	// 5. The token authenticates on the MCP endpoint...
	probe := httptest.NewRequest("POST", "https://panel.example/api/mcp", nil)
	if c := s.claimsForOAuthToken(probe, issued.AccessToken); c == nil || c.UserID != userID {
		t.Error("the issued token should resolve to the approving user")
	}
	// ...and nowhere else: a token minted for this panel must not work against
	// another address, which is the audience binding the spec requires.
	elsewhere := httptest.NewRequest("POST", "https://other.example/api/mcp", nil)
	if s.claimsForOAuthToken(elsewhere, issued.AccessToken) != nil {
		t.Error("token accepted for the wrong audience")
	}

	// 6. The code is single-use.
	rec = httptest.NewRecorder()
	tok2 := httptest.NewRequest("POST", "https://panel.example/oauth/token", strings.NewReader(tokenForm.Encode()))
	tok2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.handleToken(rec, tok2)
	if rec.Code == http.StatusOK {
		t.Error("an authorization code must not be redeemable twice")
	}

	// 7. Refresh rotates: the old refresh token dies with the exchange.
	refreshForm := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {issued.RefreshToken}}
	rf := httptest.NewRequest("POST", "https://panel.example/oauth/token", strings.NewReader(refreshForm.Encode()))
	rf.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.handleToken(rec, rf)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body.String())
	}
	rf2 := httptest.NewRequest("POST", "https://panel.example/oauth/token", strings.NewReader(refreshForm.Encode()))
	rf2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.handleToken(rec, rf2)
	if rec.Code == http.StatusOK {
		t.Error("a refresh token must be single-use (OAuth 2.1 rotation)")
	}
}

func TestOAuthPKCEAndRedirectRules(t *testing.T) {
	s, _ := mcpTestServer(t)

	// A redirect_uri that is neither https nor localhost is an open-redirect risk.
	for _, bad := range []string{"http://evil.example/cb", "notaurl", "ftp://x/cb"} {
		if err := validateRedirectURI(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	for _, ok := range []string{"https://claude.ai/api/mcp/auth_callback", "http://localhost:6274/cb", "http://127.0.0.1:1/cb"} {
		if err := validateRedirectURI(ok); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}

	// Authorization without PKCE must not proceed.
	s.db.Exec(`INSERT INTO oauth_clients (id, name, redirect_uris) VALUES ('c1','X','["https://claude.ai/cb"]')`)
	q := url.Values{"response_type": {"code"}, "client_id": {"c1"}, "redirect_uri": {"https://claude.ai/cb"}}
	req := httptest.NewRequest("GET", "https://panel.example/oauth/authorize?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	s.handleAuthorize(rec, req)
	if rec.Code == http.StatusOK {
		t.Error("authorize without a code_challenge should not render consent")
	}
}

// The 401 has to carry the resource-metadata pointer, or a client has no way to
// discover the OAuth flow — it is the first step of the whole handshake.
func TestMCPUnauthorizedAdvertisesMetadata(t *testing.T) {
	s, _ := mcpTestServer(t)
	rec := httptest.NewRecorder()
	s.unauthorized(rec, httptest.NewRequest("POST", "https://panel.example/api/mcp", nil))
	want := `Bearer resource_metadata="https://panel.example/.well-known/oauth-protected-resource"`
	if got := rec.Header().Get("WWW-Authenticate"); got != want {
		t.Errorf("WWW-Authenticate:\n got %q\nwant %q", got, want)
	}
	// Not on ordinary API routes: only the MCP endpoint speaks OAuth.
	rec = httptest.NewRecorder()
	s.unauthorized(rec, httptest.NewRequest("GET", "https://panel.example/api/servers", nil))
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Error("only the MCP endpoint should advertise resource metadata")
	}
}

// consentCSRFFrom pulls the one-shot nonce out of the rendered consent page,
// the way a browser does when it submits the form.
func consentCSRFFrom(t *testing.T, page string) string {
	t.Helper()
	const marker = `name="csrf" value="`
	i := strings.Index(page, marker)
	if i < 0 {
		t.Fatal("the consent page carries no csrf field, so the form anyone could " +
			"submit on the admin's behalf is back")
	}
	rest := page[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j <= 0 {
		t.Fatal("the csrf field is empty")
	}
	return rest[:j]
}

// The hole this closes: POST /oauth/authorize was a form with no proof it came
// from this panel. SameSite=Strict stops a cross-SITE page and the attack is
// same-site -- the panel and the apps it publishes share one base domain by
// design, so script on a shop could auto-submit decision=allow while the admin
// held a session and collect a connector token at its own redirect_uri.
func TestConsentWithoutItsNonceIsRefused(t *testing.T) {
	s, client, userID, challenge := oauthConsentFixture(t)

	for _, tc := range []struct{ name, csrf string }{
		{"no nonce at all, as a forged form would send", ""},
		{"a nonce this panel never issued", "0000000000000000000000000000000000000000000000000000000000000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, err := auth.GenerateToken(userID, "kw", "admin", 0, s.cfg.Auth.SecretKey, 24)
			if err != nil {
				t.Fatalf("session: %v", err)
			}
			form := url.Values{
				"client_id": {client.ClientID}, "redirect_uri": {"https://claude.ai/api/mcp/auth_callback"},
				"code_challenge": {challenge}, "state": {"xyz"}, "decision": {"allow"},
				"csrf": {tc.csrf},
			}
			post := httptest.NewRequest("POST", "https://panel.example/oauth/authorize", strings.NewReader(form.Encode()))
			post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			post.AddCookie(&http.Cookie{Name: "ygg_token", Value: session})
			rec := httptest.NewRecorder()

			s.handleAuthorizeSubmit(rec, post)

			if rec.Code == http.StatusFound {
				t.Errorf("a consent form with %s was accepted and issued a code: %s",
					tc.name, rec.Header().Get("Location"))
			}
		})
	}
}

// A nonce is spent on use, so a captured form cannot be replayed.
func TestConsentNonceIsSingleUse(t *testing.T) {
	s, client, userID, challenge := oauthConsentFixture(t)

	q := url.Values{
		"response_type": {"code"}, "client_id": {client.ClientID},
		"redirect_uri":   {"https://claude.ai/api/mcp/auth_callback"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
		"state": {"xyz"},
	}
	get := httptest.NewRequest("GET", "https://panel.example/oauth/authorize?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	s.handleAuthorize(rec, get)
	csrf := consentCSRFFrom(t, rec.Body.String())

	session, err := auth.GenerateToken(userID, "kw", "admin", 0, s.cfg.Auth.SecretKey, 24)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	submit := func() int {
		form := url.Values{
			"client_id": {client.ClientID}, "redirect_uri": {"https://claude.ai/api/mcp/auth_callback"},
			"code_challenge": {challenge}, "state": {"xyz"}, "decision": {"allow"}, "csrf": {csrf},
		}
		post := httptest.NewRequest("POST", "https://panel.example/oauth/authorize", strings.NewReader(form.Encode()))
		post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		post.AddCookie(&http.Cookie{Name: "ygg_token", Value: session})
		w := httptest.NewRecorder()
		s.handleAuthorizeSubmit(w, post)
		return w.Code
	}

	if first := submit(); first != http.StatusFound {
		t.Fatalf("the first submit should succeed, got %d", first)
	}
	if second := submit(); second == http.StatusFound {
		t.Error("the same nonce was accepted twice — a captured form can be replayed")
	}
}

// oauthConsentFixture gives a panel, a registered client, a user and a PKCE
// challenge — everything the consent step needs and nothing it does not.
func oauthConsentFixture(t *testing.T) (s *Server, client struct {
	ClientID string `json:"client_id"`
}, userID, challenge string) {
	t.Helper()
	s, userID = mcpTestServer(t)

	reg := httptest.NewRequest("POST", "https://panel.example/oauth/register",
		strings.NewReader(`{"redirect_uris":["https://claude.ai/api/mcp/auth_callback"],"client_name":"Claude"}`))
	reg.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleOAuthRegister(rec, reg)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	json.Unmarshal(rec.Body.Bytes(), &client) //nolint:errcheck
	if client.ClientID == "" {
		t.Fatal("no client_id issued")
	}

	sum := sha256.Sum256([]byte("a-verifier-long-enough-to-be-realistic-0123456789"))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return s, client, userID, challenge
}

// A connector token authenticated every route in the panel.
//
// The only check was on the token's AUDIENCE string, and the claims it produced
// carry no scope for scopeAllows to enforce, so the middleware waved it through
// for any path. The consent screen says the connector can "read your servers and
// their logs, and start, stop or restart them"; the same token also reached
// /api/panel/export, which decrypts every integration secret, TOTP secret and
// password hash the panel holds.
//
// Driven through authMiddleware rather than a handler, because the hole was in
// the middleware and a handler test cannot see it.
func TestConnectorTokenIsConfinedToTheMCPEndpoint(t *testing.T) {
	s, userID := mcpTestServer(t)

	const token = oauthTokenPrefix + "test-connector-token"
	if _, err := s.db.Exec(
		`INSERT INTO oauth_tokens (token_hash, refresh_hash, client_id, user_id, resource, expires_at)
		 VALUES (?,?,?,?,?,?)`,
		auth.HashToken(token), auth.HashToken("refresh"), "c1", userID,
		"https://panel.example/api/mcp", time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	var reached bool
	guarded := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	for _, tc := range []struct {
		path string
		want bool
		why  string
	}{
		{"/api/mcp", true, "the endpoint the token was issued for"},
		{"/api/panel/export", false, "decrypts every secret in the panel"},
		{"/api/servers", false, "every server, with env"},
		{"/api/users", false, "user administration"},
		{"/api/mcp/connections", false, "revokes connectors — a prefix check would allow it"},
		{"/api/mcp/info", false, "for the signed-in web UI, not for a connector"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest("POST", "https://panel.example"+tc.path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()

			guarded.ServeHTTP(rec, req)

			if reached != tc.want {
				if tc.want {
					t.Errorf("the connector token was refused on %s (%s) — the connector is broken", tc.path, tc.why)
				} else {
					t.Errorf("the connector token reached %s, which %s", tc.path, tc.why)
				}
			}
		})
	}
}

// A token whose audience is empty used to skip the audience check altogether,
// making it valid against every address the panel answers on.
func TestConnectorTokenWithNoAudienceIsRefused(t *testing.T) {
	s, userID := mcpTestServer(t)

	const token = oauthTokenPrefix + "audience-less-token"
	if _, err := s.db.Exec(
		`INSERT INTO oauth_tokens (token_hash, refresh_hash, client_id, user_id, resource, expires_at)
		 VALUES (?,?,?,?,'',?)`,
		auth.HashToken(token), auth.HashToken("r"), "c1", userID,
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	req := httptest.NewRequest("POST", "https://panel.example/api/mcp", nil)
	if c := s.claimsForOAuthToken(req, token); c != nil {
		t.Error("a token with no audience was accepted, so it is valid on every address this panel answers on")
	}
}
