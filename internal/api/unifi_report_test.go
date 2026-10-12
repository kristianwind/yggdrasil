package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/auth"

	"github.com/kristianwind/yggdrasil/internal/crypto"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// A server with "auto forward" on was unreachable from the internet for a day
// while the panel showed the setting as on, the log said nothing at all, and
// there was no way to tell "the rule was never attempted" from "the router
// refused it". The UniFi login had been answering HTTP 403 on every start.
//
// There is no UniFi controller in a unit test, so what is asserted here is the
// thing that was actually missing: that every failure path has somewhere to go.
// The behaviour itself was measured against the real gateway.
func TestUniFiFailureIsReported(t *testing.T) {
	s := testServer(t)
	id := "6dc4ed2c-0000-0000-0000-000000000000"
	if _, err := s.db.Exec(
		`INSERT INTO servers (id, name, gameskill_id, data_dir, status) VALUES (?,?,?,?,'running')`,
		id, "HogDay", "minecraft-java", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	s.logUniFi(id, "HogDay", "open", nil, nil, "login failed (HTTP 403) — check URL/credentials")

	var action, detail string
	err := s.db.QueryRow(
		`SELECT action, COALESCE(detail_json,'') FROM audit_log WHERE resource=? ORDER BY rowid DESC LIMIT 1`,
		"server:"+id).Scan(&action, &detail)
	if err != nil {
		t.Fatalf("nothing was written to the audit log: %v", err)
	}
	if action != "unifi.open.failed" {
		t.Errorf("audit action = %q, want unifi.open.failed", action)
	}
	// The reason has to survive into the record. "UniFi failed" sends the admin
	// to the wrong place; "HTTP 403" sends them to the credentials.
	if !strings.Contains(detail, "403") {
		t.Errorf("audit detail does not carry the reason: %q", detail)
	}
}

// A panel with no UniFi configured promised nothing, so it must not file a
// failure on every server start — an alert that fires for everyone who never
// asked for the feature is one that gets switched off.
func TestNoUniFiConfiguredIsNotAFailure(t *testing.T) {
	s := testServer(t)
	id := "aaaaaaaa-0000-0000-0000-000000000000"
	s.db.Exec(`INSERT INTO servers (id, name, gameskill_id, data_dir, status) VALUES (?,?,?,?,'running')`, //nolint:errcheck
		id, "Quiet", "minecraft-java", t.TempDir())

	s.logUniFi(id, "Quiet", "opened", nil, nil, "")

	// The error is checked, not discarded: the first version of this test asked a
	// column that does not exist, Scan failed, n stayed 0, and the test passed —
	// a green over a query that never ran.
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE resource=?`, "server:"+id).Scan(&n); err != nil {
		t.Fatalf("count query failed, so this test proves nothing: %v", err)
	}
	if n != 0 {
		t.Errorf("logUniFi wrote %d audit rows for a no-op, want 0", n)
	}
}

// The RCON port is published to 127.0.0.1 only. Printing it beside the public
// hostname invited someone to connect to an address that cannot answer, and then
// to read the failure as the server being broken.
//
// This drives the real handler. The first version re-implemented the loop in the
// test and was green over a planted `if false` in the code it was meant to be
// checking — a correct answer to a question nobody had asked.
func TestAdminPortIsReportedAsLocalOnly(t *testing.T) {
	s := testServer(t)
	id := "cccccccc-0000-0000-0000-000000000000"
	const runeYAML = `gameskill:
  id: mc-test
  name: MC Test
  version: 1
  docker: {image: "eclipse-temurin:21-jre"}
  startup: {command: "true"}
  ports:
    - {name: game, default: 25565, protocol: tcp}
    - {name: rcon, default: 25575, protocol: tcp}
`
	if _, err := s.db.Exec(`INSERT INTO gameskills (id, name, category, version, yaml_blob, builtin) VALUES ('mc-test','MC Test','Minecraft',1,?,1)`, runeYAML); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO servers (id, name, gameskill_id, data_dir, status, ports_json) VALUES (?,?,?,?,'running','{"game":25010,"rcon":25011}')`,
		id, "HogDay", "mc-test", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/api/servers/"+id, nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
	ctx = withClaims(ctx, &auth.Claims{UserID: "a1", Username: "admin", Role: "admin"})
	w := httptest.NewRecorder()
	s.handleGetServer(w, r.WithContext(ctx))
	if w.Code != 200 {
		t.Fatalf("GET server returned %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		LocalOnlyPorts []string `json:"local_only_ports"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.LocalOnlyPorts) != 1 || got.LocalOnlyPorts[0] != "rcon" {
		t.Errorf("local_only_ports = %v, want [rcon] — the UI would print the public host for a port bound to loopback", got.LocalOnlyPorts)
	}

	// And the binding it describes: the badge and the port mapping must not be
	// able to say different things.
	gs, err := gameskill.Parse([]byte(runeYAML))
	if err != nil {
		t.Fatal(err)
	}
	for _, pm := range serverPortMappings(gs, map[string]int{"game": 25010, "rcon": 25011}) {
		if pm.ContainerPort == 25575 && pm.HostIP != "127.0.0.1" {
			t.Errorf("rcon is published on %q, so calling it local-only would be a lie", pm.HostIP)
		}
		if pm.ContainerPort == 25565 && pm.HostIP != "" {
			t.Errorf("the game port is bound to %q; it must stay reachable", pm.HostIP)
		}
	}
}

// The test above proves the reporting works; this one proves the call site uses
// it, which is the half that was missing. UniFi is pointed at a port where
// nothing is listening, so Login fails for real — no controller required, and no
// assumption about what one would say.
func TestUniFiAddServerReportsAnUnreachableController(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	id := "bbbbbbbb-0000-0000-0000-000000000000"
	if _, err := s.db.Exec(
		`INSERT INTO servers (id, name, gameskill_id, data_dir, status, ports_json) VALUES (?,?,?,?,'running','{"game":25010}')`,
		id, "HogDay", "minecraft-java", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	// testServer leaves the cipher nil; unifiClient has to decrypt the stored
	// password, so give this one a real key rather than reach around the path
	// under test.
	ciph, err := crypto.New("unit-test-key-at-least-16")
	if err != nil {
		t.Fatal(err)
	}
	s.cipher = ciph
	enc, err := ciph.Encrypt("not-the-real-one")
	if err != nil {
		t.Fatal(err)
	}
	s.setSetting(ctx, "unifi_enabled", "1")
	s.setSetting(ctx, "unifi_url", "https://127.0.0.1:1") // nothing listens here
	s.setSetting(ctx, "unifi_user", "probe")
	s.setSetting(ctx, "unifi_pass", enc)

	s.unifiAddServer(id, "HogDay")

	var action, detail string
	if err := s.db.QueryRow(
		`SELECT action, COALESCE(detail_json,'') FROM audit_log WHERE resource=? ORDER BY rowid DESC LIMIT 1`,
		"server:"+id).Scan(&action, &detail); err != nil {
		t.Fatalf("unifiAddServer failed silently — nothing in the audit log: %v", err)
	}
	if !strings.HasPrefix(action, "unifi.") || !strings.Contains(action, "failed") {
		t.Errorf("audit action = %q, want a unifi failure", action)
	}
	if !strings.Contains(detail, "connect") && !strings.Contains(detail, "refused") {
		t.Errorf("the reason did not survive into the record: %q", detail)
	}
}

// "The integration is switched off here" and "the router did not answer" both
// came back 502. A 502 names an upstream that failed, so it sends somebody to
// look at their gateway when the answer is a checkbox on the page they are
// already on — which is what happened, at a cost of one round trip. The panel
// never contacted anything: 1.3ms, 50 bytes.
func TestUniFiOffIsNotABadGateway(t *testing.T) {
	if got := unifiErrStatus(errUnifiOff); got != 409 {
		t.Errorf("a disabled integration returned %d, want 409 — 502 blames the router", got)
	}
	// Anything the controller itself did must still read as an upstream failure.
	if got := unifiErrStatus(errors.New("connect: connection refused")); got != 502 {
		t.Errorf("a controller failure returned %d, want 502", got)
	}
	if got := unifiErrStatus(fmt.Errorf("listing rules: %w", errUnifiOff)); got != 409 {
		t.Errorf("a wrapped errUnifiOff returned %d, want 409 — errors.Is, not ==", got)
	}
}
