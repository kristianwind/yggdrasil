package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFilePathParamPrefersTheEncodedForm(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString([]byte("wp-config.php"))
	r := httptest.NewRequest(http.MethodGet, "/x?path_b64="+enc, nil)
	if got := filePathParam(r); got != "wp-config.php" {
		t.Errorf("got %q, want the decoded path", got)
	}
}

// Padded base64 is accepted too: not every client strips it, and rejecting it
// would look like an empty path rather than a bad one.
func TestFilePathParamAcceptsPadding(t *testing.T) {
	enc := base64.URLEncoding.EncodeToString([]byte("wp-content/uploads"))
	r := httptest.NewRequest(http.MethodGet, "/x?path_b64="+enc, nil)
	if got := filePathParam(r); got != "wp-content/uploads" {
		t.Errorf("got %q, want the decoded path", got)
	}
}

// The plain form still works, so existing clients, scripts and bookmarks do not
// break on upgrade.
func TestFilePathParamFallsBackToPlain(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x?path=server.properties", nil)
	if got := filePathParam(r); got != "server.properties" {
		t.Errorf("got %q, want the plain path", got)
	}
	if got := filePathParam(httptest.NewRequest(http.MethodGet, "/x", nil)); got != "" {
		t.Errorf("no parameter should mean the root, got %q", got)
	}
}

// Undecodable input falls through to ?path= rather than silently becoming the
// data directory root — otherwise a mangled request would list the whole server.
func TestFilePathParamIgnoresGarbageEncoding(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x?path_b64=!!!not-base64!!!&path=wp-config.php", nil)
	if got := filePathParam(r); got != "wp-config.php" {
		t.Errorf("got %q, want the plain path as fallback", got)
	}
}

func TestPublicURLPrefersAConfiguredDomain(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	s.db.Exec("INSERT INTO servers (id, name, gameskill_id, status, data_dir, cf_hostname) VALUES (?,?,?,?,?,?)",
		"srv-1", "site", "wordpress", "running", "/tmp/x", "shop.example.dk")

	if got := s.publicURL(ctx, "srv-1", map[string]int{"web": 25013}); got != "https://shop.example.dk" {
		t.Errorf("got %q, want the server's own domain and no port", got)
	}
}

func TestPublicURLFallsBackToHostAndPort(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	s.db.Exec("INSERT INTO servers (id, name, gameskill_id, status, data_dir) VALUES (?,?,?,?,?)",
		"srv-2", "clone", "wordpress", "stopped", "/tmp/y")
	s.setSetting(ctx, "public_hostname", "panel.example.dk")

	if got := s.publicURL(ctx, "srv-2", map[string]int{"web": 25001}); got != "http://panel.example.dk:25001" {
		t.Errorf("got %q, want host:port", got)
	}
	// A game server has no "web" port; the game port is the next best answer.
	if got := s.publicURL(ctx, "srv-2", map[string]int{"game": 25500}); got != "http://panel.example.dk:25500" {
		t.Errorf("got %q, want the game port", got)
	}
	// Some allocated port beats none at all.
	if got := s.publicURL(ctx, "srv-2", map[string]int{"rcon": 25600}); got != "http://panel.example.dk:25600" {
		t.Errorf("got %q, want any allocated port", got)
	}
	if got := s.publicURL(ctx, "srv-2", nil); got != "http://panel.example.dk" {
		t.Errorf("got %q, want the bare host when nothing is allocated", got)
	}
}
