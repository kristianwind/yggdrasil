package api

import (
	"net/http"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/auth"
)

// A scope is a ceiling, never a grant. It can only take authority away from
// what the role already allows, so an unscoped token — every token that existed
// before scopes, and the default for new ones — must be completely unaffected.
func TestUnscopedTokensAreUnaffected(t *testing.T) {
	c := &auth.Claims{Role: "admin"}
	for _, p := range []string{"/api/servers", "/api/servers/x/export", "/api/settings/cloudflare", "/api/users"} {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			if !scopeAllows(c, m, p) {
				t.Errorf("%s %s denied for an unscoped token — scopes must not change existing behaviour", m, p)
			}
		}
	}
}

// The transfer scope exists so a panel can hold another panel's token without
// holding admin on it. It may read what a pull needs and nothing else.
func TestTransferScopeAllowsOnlyWhatAPullNeeds(t *testing.T) {
	c := &auth.Claims{Role: "admin", Scope: auth.ScopeTransfer}

	allowed := []struct{ method, path string }{
		{http.MethodGet, "/api/servers"},
		{http.MethodGet, "/api/servers/9f0c1b2a-0000-0000-0000-000000000000/export"},
	}
	for _, a := range allowed {
		if !scopeAllows(c, a.method, a.path) {
			t.Errorf("%s %s must be allowed — a pull cannot work without it", a.method, a.path)
		}
	}

	denied := []struct{ method, path string }{
		// The obvious ones.
		{http.MethodPost, "/api/servers/x/stop"},
		{http.MethodPost, "/api/servers/x/restart"},
		{http.MethodDelete, "/api/servers/x"},
		{http.MethodGet, "/api/settings/cloudflare"},
		{http.MethodGet, "/api/users"},
		{http.MethodGet, "/api/audit"},
		{http.MethodGet, "/api/servers/x/console"},
		{http.MethodGet, "/api/servers/x/files"},
		// Releasing another panel's hostnames is exactly the kind of authority a
		// saved token must not carry: it takes a live site off the internet.
		{http.MethodPost, "/api/servers/x/release-domains"},
		// A POST to the export path would let the token cause work on the source.
		// The allowlist is on the method as well as the path for this reason.
		{http.MethodPost, "/api/servers/x/export"},
		// Close to the allowed shape, but not it.
		{http.MethodGet, "/api/servers/x/export/something"},
		{http.MethodGet, "/api/panel/export"},
	}
	for _, d := range denied {
		if scopeAllows(c, d.method, d.path) {
			t.Errorf("%s %s must be denied for a transfer token", d.method, d.path)
		}
	}
}

// A scope this build does not recognise must fail closed. If a newer version
// adds one and the token reaches an older panel, "unknown" must not read as
// "unrestricted" — that would turn a narrowing feature into a widening one.
func TestUnknownScopeIsRefusedEverywhere(t *testing.T) {
	c := &auth.Claims{Role: "admin", Scope: "some-future-scope"}
	for _, p := range []string{"/api/servers", "/api/servers/x/export", "/api/settings/cloudflare"} {
		if scopeAllows(c, http.MethodGet, p) {
			t.Errorf("GET %s allowed for an unrecognised scope — it must fail closed", p)
		}
	}
}

// Nil claims are the unauthenticated path, which the middleware rejects before
// this is reached. It must not panic if it ever is.
func TestScopeAllowsHandlesNilClaims(t *testing.T) {
	if !scopeAllows(nil, http.MethodGet, "/api/servers") {
		t.Error("nil claims must fall through to the normal auth checks, not be denied here")
	}
}
