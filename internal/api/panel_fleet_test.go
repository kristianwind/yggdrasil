package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/auth"
)

// A link token is what "one login for several hosts" runs on. It must be able
// to show and operate a host, and nothing more — linking a machine should not
// hand the controller the keys to it.
func TestLinkScopeCanOperateButNotOwn(t *testing.T) {
	c := &auth.Claims{Role: "admin", Scope: auth.ScopeLink}

	allowed := []struct{ method, path string }{
		{http.MethodGet, "/api/servers"},
		{http.MethodGet, "/api/fleet/summary"},
		{http.MethodGet, "/api/fleet/metrics"},
		{http.MethodGet, "/api/servers/abc"},
		{http.MethodGet, "/api/servers/abc/stats"},
		{http.MethodGet, "/api/servers/abc/history"},
		{http.MethodPost, "/api/servers/abc/start"},
		{http.MethodPost, "/api/servers/abc/stop"},
		{http.MethodPost, "/api/servers/abc/restart"},
		{http.MethodPost, "/api/servers/abc/safe-restart"},
	}
	for _, a := range allowed {
		if !scopeAllows(c, a.method, a.path) {
			t.Errorf("%s %s must be allowed — a linked panel needs it", a.method, a.path)
		}
	}

	denied := []struct{ method, path string }{
		// Exporting is a different grant on purpose: a bundle carries decrypted
		// secrets, and a controller that presses Start has no business reading
		// them. That is ScopeTransfer's job, and the two do not overlap.
		{http.MethodGet, "/api/servers/abc/export"},
		{http.MethodDelete, "/api/servers/abc"},
		{http.MethodPost, "/api/servers/abc/release-domains"},
		{http.MethodPost, "/api/servers/abc/takeover-domains"},
		{http.MethodGet, "/api/servers/abc/files"},
		{http.MethodGet, "/api/servers/abc/console"},
		{http.MethodGet, "/api/servers/abc/backups"},
		{http.MethodGet, "/api/settings/cloudflare"},
		{http.MethodGet, "/api/users"},
		{http.MethodGet, "/api/audit"},
		{http.MethodPost, "/api/servers/abc/install"},
		{http.MethodPut, "/api/servers/abc"},
	}
	for _, d := range denied {
		if scopeAllows(c, d.method, d.path) {
			t.Errorf("%s %s must be denied for a link token", d.method, d.path)
		}
	}
}

// The allowlist matches on the path SEGMENT, not a suffix. A rule written as
// "ends in /start" would also match a path nobody intended.
func TestServerSubpathParsesRatherThanMatchesSuffixes(t *testing.T) {
	cases := []struct {
		in, id, rest string
		ok           bool
	}{
		{"/api/servers/abc", "abc", "", true},
		{"/api/servers/abc/stats", "abc", "stats", true},
		{"/api/servers/abc/files/deep/path", "abc", "files/deep/path", true},
		{"/api/servers/", "", "", false},
		{"/api/servers", "", "", false},
		{"/api/panel/export", "", "", false},
	}
	for _, c := range cases {
		id, rest, ok := serverSubpath(c.in)
		if id != c.id || rest != c.rest || ok != c.ok {
			t.Errorf("serverSubpath(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, id, rest, ok, c.id, c.rest, c.ok)
		}
	}
	// The concrete trap: a nested path must not be read as the action.
	c := &auth.Claims{Role: "admin", Scope: auth.ScopeLink}
	if scopeAllows(c, http.MethodPost, "/api/servers/abc/files/start") {
		t.Error("a nested path ending in an allowed word must not be allowed")
	}
}

// One panel being down must cost that panel's rows and nothing else. A combined
// view that breaks when any one of four machines is rebooting is worse than
// four bookmarks.
func TestOnePanelDownDoesNotBreakTheOthers(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{{"id": "s1", "name": "Alfa"}}) //nolint:errcheck
	}))
	defer up.Close()

	servers, err := fetchRemoteServers(up.URL, "ygg_x")
	if err != nil || len(servers) != 1 {
		t.Fatalf("a reachable panel must list: %v %v", servers, err)
	}

	// Closed port: reported, not fatal.
	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := down.URL
	down.Close()
	if _, err := fetchRemoteServers(addr, "ygg_x"); err == nil {
		t.Error("an unreachable panel must report an error")
	}

	// A rejected token says so specifically, because the fix is different: the
	// token was deleted over there, not the machine turned off.
	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer refuse.Close()
	_, err = fetchRemoteServers(refuse.URL, "ygg_stale")
	if err != errTokenRejected {
		t.Errorf("got %v, want the token-rejected error so the message can say what to fix", err)
	}
}
