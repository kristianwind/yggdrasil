package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedRoutedServer inserts a server with a primary subdomain and n extra
// hostnames, the way a website server looks once its apex and www are set.
func seedRoutedServer(t *testing.T, s *Server, id, name, subdomain string, extra ...string) {
	t.Helper()
	if _, err := s.db.Exec(
		`INSERT INTO servers (id, name, gameskill_id, status, env_json, ports_json, data_dir, subdomain)
		 VALUES (?,?, 'mc-test','stopped','{}','{"game":25565}',?,?)`,
		id, name, "/tmp/"+id, subdomain); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	for _, h := range extra {
		if _, err := s.db.Exec(
			"INSERT INTO server_routes (id, server_id, hostname, port_name) VALUES (?,?,?,'')",
			id+"-"+h, id, h); err != nil {
			t.Fatalf("seed route %s: %v", h, err)
		}
	}
}

// A website's routing is mostly NOT its primary subdomain: it is the apex and the
// www beside it, which live in server_routes. The manifest carried one field, so
// a move used to leave both behind — the target looked migrated and served
// nothing on the names anyone actually types.
func TestTransferCarriesExtraHostnames(t *testing.T) {
	ctx := context.Background()
	src := transferTestServer(t, "source-key-0123456789abcdef-xyz")
	src.db.Exec("INSERT OR IGNORE INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('mc-test','MC Test','game',1,?,0)", migrationTestRune)
	seedRoutedServer(t, src, "srv-web", "3dekoration.dk", "3dekoration",
		"3dekoration.dk", "www.3dekoration.dk")

	man, _, err := src.buildTransferManifest(ctx, "srv-web")
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if man.Version != transferVersion {
		t.Errorf("version = %d, want %d", man.Version, transferVersion)
	}
	got := map[string]bool{}
	for _, r := range man.Routes {
		got[r.Hostname] = true
	}
	for _, want := range []string{"3dekoration.dk", "www.3dekoration.dk"} {
		if !got[want] {
			t.Errorf("manifest does not carry %q — the move would leave it behind", want)
		}
	}

	// And they have to land on the target.
	dst := transferTestServer(t, "target-key-fedcba9876543210-abc")
	dst.db.Exec("INSERT OR IGNORE INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('mc-test','MC Test','game',1,?,0)", migrationTestRune)
	dst.db.Exec(`INSERT INTO servers (id, name, gameskill_id, status, env_json, ports_json, data_dir)
	             VALUES ('new','Copy','mc-test','stopped','{}','{}','/tmp/new')`)
	restored, dropped, routesDropped := dst.restoreServerTail(ctx, "new", man)
	if len(routesDropped) != 0 || dropped != "" {
		t.Fatalf("nothing should clash on an empty target: dropped=%q routes=%v", dropped, routesDropped)
	}
	var n int
	dst.db.QueryRow("SELECT COUNT(*) FROM server_routes WHERE server_id='new'").Scan(&n)
	if n != 2 {
		t.Errorf("target has %d extra hostnames, want 2", n)
	}
	if !strings.Contains(strings.Join(restored, ", "), "hostnames") {
		t.Errorf("the import must report what it restored, got %v", restored)
	}

	// The route is recorded but NOT provisioned: what the source has in its tunnel
	// says nothing about what the target has in its own, and a fresh copy must not
	// claim to own rules it has never created.
	var prov string
	dst.db.QueryRow("SELECT COALESCE(cf_hostname,'') FROM server_routes WHERE server_id='new' LIMIT 1").Scan(&prov)
	if prov != "" {
		t.Errorf("imported route claims to be provisioned (%q) — it has never been created on this panel", prov)
	}
}

// A hostname another server here already serves must not be silently taken: the
// panel cannot resolve two claims on one name, and whichever started last would
// win the tunnel rule.
func TestTransferDropsHostnamesAlreadyClaimedHere(t *testing.T) {
	ctx := context.Background()
	dst := transferTestServer(t, "target-key-fedcba9876543210-abc")
	dst.db.Exec("INSERT OR IGNORE INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('mc-test','MC Test','game',1,?,0)", migrationTestRune)
	seedRoutedServer(t, dst, "sitting", "Already here", "", "www.3dekoration.dk")
	dst.db.Exec(`INSERT INTO servers (id, name, gameskill_id, status, env_json, ports_json, data_dir)
	             VALUES ('new','Copy','mc-test','stopped','{}','{}','/tmp/new')`)

	man := &transferManifest{Version: transferVersion, Routes: []transferRoute{
		{Hostname: "3dekoration.dk"},
		{Hostname: "www.3dekoration.dk"},
	}}
	_, _, routesDropped := dst.restoreServerTail(ctx, "new", man)
	if len(routesDropped) != 1 || routesDropped[0] != "www.3dekoration.dk" {
		t.Fatalf("dropped = %v, want exactly the clashing hostname reported back", routesDropped)
	}
	var n int
	dst.db.QueryRow("SELECT COUNT(*) FROM server_routes WHERE server_id='new'").Scan(&n)
	if n != 1 {
		t.Errorf("target took %d hostnames, want only the free one", n)
	}
}

// The manifest is JSON on the wire between two panels that may be on different
// versions. A v2 source has no "routes" key at all, and that must import as "no
// extra hostnames" rather than an error.
func TestManifestWithoutRoutesStillImports(t *testing.T) {
	ctx := context.Background()
	dst := transferTestServer(t, "target-key-fedcba9876543210-abc")
	dst.db.Exec(`INSERT INTO servers (id, name, gameskill_id, status, env_json, ports_json, data_dir)
	             VALUES ('new','Copy','mc-test','stopped','{}','{}','/tmp/new')`)

	var man transferManifest
	if err := json.Unmarshal([]byte(`{"version":2,"name":"Old","gameskill_id":"mc-test","subdomain":"shop"}`), &man); err != nil {
		t.Fatalf("a v2 manifest must still parse: %v", err)
	}
	if man.Routes != nil {
		t.Errorf("routes = %v, want nil for a v2 bundle", man.Routes)
	}
	if _, _, routesDropped := dst.restoreServerTail(ctx, "new", &man); len(routesDropped) != 0 {
		t.Errorf("a v2 bundle must not report dropped hostnames, got %v", routesDropped)
	}
}

// Handing the domains to a stopped copy would take the site DOWN rather than
// move it: the source gives up its rules, and the ones created here point at a
// port nothing is listening on. That is the single outcome this endpoint exists
// to prevent, so it is refused before the source is even contacted.
func TestTakeoverRefusesAStoppedTarget(t *testing.T) {
	s := transferTestServer(t, "target-key-fedcba9876543210-abc")
	s.db.Exec("INSERT OR IGNORE INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('mc-test','MC Test','game',1,?,0)", migrationTestRune)
	seedRoutedServer(t, s, "copy", "Copy", "shop", "example.com")

	body := `{"url":"http://127.0.0.1:1","token":"t","source_server_id":"src-1"}`
	w := httptest.NewRecorder()
	s.handleTakeoverDomains(w, adminReq(t, http.MethodPost, "/api/servers/copy/takeover-domains", body, "copy"))

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a stopped target (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "start this server first") {
		t.Errorf("the refusal must say what to do: %s", w.Body.String())
	}
}
