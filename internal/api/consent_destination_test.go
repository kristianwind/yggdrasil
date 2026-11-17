package api

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The client's name is chosen by whoever registers it. The consent page has to
// lead with the one thing they cannot choose: where the approval is sent.
func TestConsentPageNamesWhereTheApprovalGoes(t *testing.T) {
	s, _ := mcpTestServer(t)
	s.db.Exec(`INSERT INTO oauth_clients (id, name, redirect_uris) VALUES ('real','Claude','["https://claude.ai/api/mcp/auth_callback"]')`)      //nolint:errcheck
	s.db.Exec(`INSERT INTO oauth_clients (id, name, redirect_uris) VALUES ('fake','Claude','["https://claude.ai.evil.example/cb"]')`)            //nolint:errcheck
	s.db.Exec(`INSERT INTO oauth_clients (id, name, redirect_uris) VALUES ('local','MCP Inspector','["http://localhost:6274/oauth/callback"]')`) //nolint:errcheck
	page := func(client, redirect string) string {
		q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {redirect},
			"code_challenge": {"abc"}, "code_challenge_method": {"S256"}}
		rec := httptest.NewRecorder()
		s.handleAuthorize(rec, httptest.NewRequest("GET", "https://panel.example/oauth/authorize?"+q.Encode(), nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", client, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	real := page("real", "https://claude.ai/api/mcp/auth_callback")
	if !strings.Contains(real, "<b>claude.ai</b>") || strings.Contains(real, "This is not Claude") {
		t.Errorf("the real connector is not shown plainly:\n%s", real)
	}

	fake := page("fake", "https://claude.ai.evil.example/cb")
	if !strings.Contains(fake, "<b>claude.ai.evil.example</b>") {
		t.Error("the look-alike's real host is not on the page")
	}
	if !strings.Contains(fake, "This is not Claude") {
		t.Error("an app calling itself Claude, sending to another host, carries no warning")
	}
	if strings.Contains(fake, "<h1>Connect <span") {
		t.Error("the self-chosen name is still the headline")
	}

	local := page("local", "http://localhost:6274/oauth/callback")
	if !strings.Contains(local, "localhost:6274") || !strings.Contains(local, "a program on this computer") || strings.Contains(local, "This is not Claude") {
		t.Errorf("a local client is not described as one:\n%s", local)
	}
}

func TestDestinationOfDoesNotMatchBySuffix(t *testing.T) {
	for uri, known := range map[string]bool{
		"https://claude.ai/cb":                true,
		"https://CLAUDE.AI/cb":                true,
		"https://claude.com/x":                true,
		"https://claude.ai.evil.example/cb":   false,
		"https://evilclaude.ai/cb":            false,
		"https://x.claude.ai.attacker.net/cb": false,
	} {
		if got := destinationOf(uri).Known; got != known {
			t.Errorf("%s: known=%v, want %v", uri, got, known)
		}
	}
}
