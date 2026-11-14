package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kristianwind/yggdrasil/internal/auth"
	"github.com/kristianwind/yggdrasil/internal/crypto"
)

// Starting an enrolment clears the enabled flag. On an account that already has
// 2FA that is "switch it off", and it asked for no code.
func TestStartingTOTPEnrolmentCannotSwitchTwoFactorOff(t *testing.T) {
	s := testServer(t)
	cipher, err := crypto.New("test-secret-key-abcdef-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	s.cipher = cipher
	s.db.Exec("INSERT INTO users (id, username, password_hash, role, totp_secret, totp_enabled) VALUES ('u9','has2fa','x','user','KEEPME',1)") //nolint:errcheck
	r := httptest.NewRequest("POST", "/api/auth/2fa/setup", nil)
	r = r.WithContext(withClaims(r.Context(), &auth.Claims{UserID: "u9", Username: "has2fa", Role: "user"}))
	w := httptest.NewRecorder()
	s.handle2FASetup(w, r)
	var secret string
	var enabled int
	if err := s.db.QueryRow("SELECT totp_secret, totp_enabled FROM users WHERE id='u9'").Scan(&secret, &enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 || secret != "KEEPME" {
		t.Fatalf("setup changed an enabled account: enabled=%d secret=%q (HTTP %d)", enabled, secret, w.Code)
	}
	if w.Code != http.StatusConflict {
		t.Errorf("got %d %s", w.Code, w.Body.String())
	}

	// The control: an account without 2FA can still begin.
	s.db.Exec("INSERT INTO users (id, username, password_hash, role) VALUES ('u8','fresh','x','user')") //nolint:errcheck
	r = httptest.NewRequest("POST", "/api/auth/2fa/setup", nil)
	r = r.WithContext(withClaims(r.Context(), &auth.Claims{UserID: "u8", Username: "fresh", Role: "user"}))
	w = httptest.NewRecorder()
	s.handle2FASetup(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "otpauth://") {
		t.Errorf("a fresh account could not start enrolment: %d %s", w.Code, w.Body.String())
	}
}

func TestDemoRefusesExportsAndDownloads(t *testing.T) {
	h := demoServer(t, true)
	for _, p := range []string{
		"/api/panel/export", "/api/panel/export/", "/api/migration/export", "/api/audit/export",
		"/api/servers/abc/export", "/api/servers/abc/files/download", "/api/backups/b1/download",
	} {
		if got := do(h, http.MethodGet, p+"?include=users"); got.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d — a demo whose admin password is public must not hand this out", p, got.Code)
		}
	}
	for _, p := range []string{"/api/servers", "/api/servers/abc", "/api/servers/abc/files", "/api/servers/abc/backups", "/api/servers/abc/logs/export"} {
		if got := do(h, http.MethodGet, p); got.Code != http.StatusOK {
			t.Errorf("GET %s = %d — still a read", p, got.Code)
		}
	}
	off := demoServer(t, false)
	if got := do(off, http.MethodGet, "/api/panel/export"); got.Code != http.StatusOK {
		t.Errorf("an ordinary install was affected: %d", got.Code)
	}
}

func TestTransferScopeDoesNotReachLogs(t *testing.T) {
	c := &auth.Claims{Role: "admin", Scope: auth.ScopeTransfer}
	for _, p := range []string{"/api/servers/x/logs/export", "/api/servers/x/install/log/export", "/api/servers//export", "/api/servers/export"} {
		if scopeAllows(c, http.MethodGet, p) {
			t.Errorf("a transfer token may GET %s", p)
		}
	}
	if !scopeAllows(c, http.MethodGet, "/api/servers/x/export") {
		t.Error("a transfer token can no longer export a server")
	}
}

// Both parameter checks answer by redirecting to redirect_uri. They ran before
// anything had established that redirect_uri was this client's.
func TestAuthorizeDoesNotRedirectToAnUnregisteredAddress(t *testing.T) {
	s, _ := mcpTestServer(t)
	s.db.Exec(`INSERT INTO oauth_clients (id, name, redirect_uris) VALUES ('c1','X','["https://claude.ai/cb"]')`) //nolint:errcheck
	for name, q := range map[string]url.Values{
		"no client":              {"redirect_uri": {"https://evil.example/x"}},
		"unknown client":         {"client_id": {"nope"}, "redirect_uri": {"https://evil.example/x"}},
		"real client, other uri": {"client_id": {"c1"}, "redirect_uri": {"https://evil.example/x"}},
	} {
		rec := httptest.NewRecorder()
		s.handleAuthorize(rec, httptest.NewRequest("GET", "https://panel.example/oauth/authorize?"+q.Encode(), nil))
		if loc := rec.Header().Get("Location"); loc != "" || rec.Code/100 == 3 {
			t.Errorf("%s: redirected (%d) to %q", name, rec.Code, loc)
		}
	}
	// The control: a registered address still gets its error delivered by redirect.
	q := url.Values{"client_id": {"c1"}, "redirect_uri": {"https://claude.ai/cb"}, "response_type": {"token"}}
	rec := httptest.NewRecorder()
	s.handleAuthorize(rec, httptest.NewRequest("GET", "https://panel.example/oauth/authorize?"+q.Encode(), nil))
	if !strings.HasPrefix(rec.Header().Get("Location"), "https://claude.ai/cb?") {
		t.Errorf("a registered client's error was not redirected: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestOAuthRegistrationIsBounded(t *testing.T) {
	s, _ := mcpTestServer(t)
	post := func(body, ip string) int {
		r := httptest.NewRequest("POST", "https://panel.example/oauth/register", strings.NewReader(body))
		r.RemoteAddr = ip
		w := httptest.NewRecorder()
		s.handleOAuthRegister(w, r)
		return w.Code
	}
	ok := `{"redirect_uris":["https://claude.ai/cb"]}`
	if c := post(`{"redirect_uris":["https://claude.ai/`+strings.Repeat("a", 3000)+`"]}`, "198.51.100.1"); c != 400 {
		t.Errorf("a 3 KB redirect_uri: %d", c)
	}
	many := `{"redirect_uris":[` + strings.TrimSuffix(strings.Repeat(`"https://claude.ai/cb",`, 11), ",") + `]}`
	if c := post(many, "198.51.100.2"); c != 400 {
		t.Errorf("eleven redirect_uris: %d", c)
	}
	// Per-address limit.
	limited := false
	for i := 0; i < 8; i++ {
		if post(ok, "198.51.100.3") == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Error("eight registrations in a row from one address were all accepted")
	}
	// And a cap that does not depend on the address: idle clients beyond it go,
	// oldest first; one that holds a token stays however old it is.
	s.db.Exec(`INSERT INTO oauth_clients (id, name, redirect_uris, created_at) VALUES ('old-in-use','X','[]','2020-01-01 00:00:00')`) //nolint:errcheck
	if _, err := s.db.Exec(`INSERT INTO oauth_tokens (token_hash, refresh_hash, client_id, user_id, resource, expires_at) VALUES ('h','r','old-in-use','u1','x','2099-01-01 00:00:00')`); err != nil {
		t.Fatalf("seed token (schema changed?): %v", err)
	}
	for i := 0; i < oauthMaxIdleClients+50; i++ {
		s.db.Exec(`INSERT INTO oauth_clients (id, name, redirect_uris, created_at) VALUES (?,?,'[]','2021-01-01 00:00:00')`, "idle-"+time.Duration(i).String(), "X") //nolint:errcheck
	}
	s.pruneIdleOAuthClients(context.Background())
	var idle, inUse int
	s.db.QueryRow("SELECT COUNT(*) FROM oauth_clients WHERE id<>'old-in-use'").Scan(&idle) //nolint:errcheck
	s.db.QueryRow("SELECT COUNT(*) FROM oauth_clients WHERE id='old-in-use'").Scan(&inUse) //nolint:errcheck
	if idle != oauthMaxIdleClients || inUse != 1 {
		t.Errorf("after pruning: %d idle clients (want %d), in-use kept=%d", idle, oauthMaxIdleClients, inUse)
	}
}

func TestResetMailCooldownIsPerAccount(t *testing.T) {
	c := &cooldown{window: time.Minute, last: map[string]time.Time{}}
	if !c.allow("a") || c.allow("a") {
		t.Error("the second mail to one account inside the window was allowed")
	}
	if !c.allow("b") {
		t.Error("one account's cooldown held back another's")
	}
}
