package api

import (
	"strings"
	"testing"

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
// host invited someone to connect to an address that cannot answer, and then to
// read the failure as the server being broken.
func TestAdminPortIsReportedAsLocalOnly(t *testing.T) {
	gs := &gameskill.Gameskill{Ports: []gameskill.Port{
		{Name: "game", Default: 25565, Protocol: "tcp"},
		{Name: "rcon", Default: 25575, Protocol: "tcp"},
	}}
	var local []string
	for _, p := range gs.Ports {
		if isAdminPort(p.Name) {
			local = append(local, p.Name)
		}
	}
	if len(local) != 1 || local[0] != "rcon" {
		t.Errorf("local-only ports = %v, want [rcon]", local)
	}
	// And the mapping it is derived from really does bind loopback, so the badge
	// and the binding cannot say different things.
	for _, pm := range serverPortMappings(gs, map[string]int{"game": 25010, "rcon": 25011}) {
		if pm.ContainerPort == 25575 && pm.HostIP != "127.0.0.1" {
			t.Errorf("rcon is published on %q, so calling it local-only would be a lie", pm.HostIP)
		}
		if pm.ContainerPort == 25565 && pm.HostIP != "" {
			t.Errorf("the game port is bound to %q; it must stay reachable", pm.HostIP)
		}
	}
}
