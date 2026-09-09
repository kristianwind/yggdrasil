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

// A variable whose DEFAULT is "{{PUBLIC_URL}}" must reach the container
// expanded, not as the literal braces.
//
// The docs only promise this for a value an operator types into the form, and
// the substitution loop reads that way. It works for defaults too, but only
// because of an ordering that is easy to lose: loadRuntime seeds
// gameskill.DefaultEnv BEFORE the loop runs, so a default is already in the map
// by the time anything looks for the placeholder. Move the seeding after the
// loop — or expand only the keys present in env_json, which is the obvious
// reading of "values the admin typed" — and every rune relying on it starts
// handing its app the four characters "{{PU" instead of an address, with nothing
// failing until the app itself does.
//
// The OpenCloud rune depends on this: its identity provider refuses to start on
// anything but an https URL, so its OC_URL default is the panel's own answer.
func TestPublicURLExpandsInAVariableDefault(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	yaml := "gameskill:\n" +
		"  id: testurl\n  name: TestURL\n  docker: { image: x }\n" +
		"  startup: { command: run }\n" +
		"  variables:\n" +
		"    - { key: SITE_URL, name: Address, type: string, default: \"{{PUBLIC_URL}}\" }\n" +
		"    - { key: CALLBACK, name: Callback, type: string, default: \"{{PUBLIC_URL}}/oauth\" }\n" +
		"  ports:\n    - { name: web, default: 80, protocol: tcp }\n"
	if _, err := s.db.Exec("INSERT INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('testurl','TestURL','t',1,?,1)", yaml); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		"INSERT INTO servers (id,name,gameskill_id,status,env_json,ports_json,data_dir) VALUES (?,?,?,?,?,?,?)",
		"srv-url", "app", "testurl", "stopped", "{}", `{"web":25010}`, "/tmp/z"); err != nil {
		t.Fatal(err)
	}
	s.setSetting(ctx, "public_hostname", "panel.example.dk")

	rt, err := s.loadRuntime(ctx, "srv-url")
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.env["SITE_URL"]; got != "http://panel.example.dk:25010" {
		t.Errorf("SITE_URL = %q, want the expanded address — a default is not expanded", got)
	}
	// Substitution, not assignment: the placeholder can sit inside a longer value.
	if got := rt.env["CALLBACK"]; got != "http://panel.example.dk:25010/oauth" {
		t.Errorf("CALLBACK = %q, want the placeholder replaced in place", got)
	}
	if got := rt.env["PUBLIC_URL"]; got != "http://panel.example.dk:25010" {
		t.Errorf("PUBLIC_URL = %q, want it exported in its own right", got)
	}
}

// The documented case: a value the operator typed. Untested until now, and it
// shares its one loop with the defaults above.
func TestPublicURLExpandsInAnOperatorTypedValue(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	yaml := "gameskill:\n" +
		"  id: testurl2\n  name: TestURL2\n  docker: { image: x }\n" +
		"  startup: { command: run }\n" +
		"  variables:\n    - { key: SITE_URL, name: Address, type: string, default: \"\" }\n" +
		"  ports:\n    - { name: web, default: 80, protocol: tcp }\n"
	if _, err := s.db.Exec("INSERT INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('testurl2','TestURL2','t',1,?,1)", yaml); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		"INSERT INTO servers (id,name,gameskill_id,status,env_json,ports_json,data_dir,cf_hostname) VALUES (?,?,?,?,?,?,?,?)",
		"srv-url2", "site", "testurl2", "stopped", `{"SITE_URL":"{{PUBLIC_URL}}"}`, `{"web":25011}`, "/tmp/z2",
		"shop.example.dk"); err != nil {
		t.Fatal(err)
	}

	rt, err := s.loadRuntime(ctx, "srv-url2")
	if err != nil {
		t.Fatal(err)
	}
	// A domain wins over host:port, and it is https — which is the difference a
	// rune like OpenCloud reads to decide who terminates TLS.
	if got := rt.env["SITE_URL"]; got != "https://shop.example.dk" {
		t.Errorf("SITE_URL = %q, want the server's own domain", got)
	}
}
