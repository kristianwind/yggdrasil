package api

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func seedRouteServer(t *testing.T, s *Server, id, sub, subPort string) {
	t.Helper()
	s.db.Exec("INSERT OR IGNORE INTO gameskills (id, name, yaml) VALUES ('gs','gs','')")
	if _, err := s.db.Exec(
		"INSERT INTO servers (id, name, gameskill_id, status, data_dir, subdomain, subdomain_port) VALUES (?,?,'gs','running','/tmp/x',?,?)",
		id, "srv-"+id, sub, subPort); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// The primary must come first and be marked as such: everything downstream keys
// off it to decide whether provisioned state belongs on the servers row or in
// server_routes, and getting that wrong writes one route's hostname over
// another's teardown record.
func TestServerRoutesPutsThePrimaryFirst(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	seedRouteServer(t, s, "a", "notes", "")
	s.db.Exec("INSERT INTO server_routes (id, server_id, hostname, port_name) VALUES (?,?,?,?)",
		uuid.New().String(), "a", "zzz.example.com", "admin")
	s.db.Exec("INSERT INTO server_routes (id, server_id, hostname, port_name) VALUES (?,?,?,?)",
		uuid.New().String(), "a", "aaa.example.com", "")

	got := s.serverRoutes(ctx, "a")
	if len(got) != 3 {
		t.Fatalf("got %d routes, want 3", len(got))
	}
	if !got[0].Primary || got[0].Hostname != "notes" {
		t.Errorf("first route = %+v, want the primary 'notes'", got[0])
	}
	if got[0].ID != "" {
		t.Error("the primary must have an empty id — it does not live in server_routes")
	}
	for _, r := range got[1:] {
		if r.Primary || r.ID == "" {
			t.Errorf("extra route %+v must not be primary and must carry an id", r)
		}
	}
	// Extras are ordered by hostname, so the list does not reshuffle between reads.
	if got[1].Hostname != "aaa.example.com" || got[2].Hostname != "zzz.example.com" {
		t.Errorf("extras out of order: %q, %q", got[1].Hostname, got[2].Hostname)
	}
}

// A server with no hostname at all yields nothing — the providers use an empty
// list as "there is nothing to provision", which is how a server without a
// subdomain has always been skipped.
func TestServerRoutesEmptyWhenNothingConfigured(t *testing.T) {
	s := testServer(t)
	seedRouteServer(t, s, "b", "", "")
	if got := s.serverRoutes(context.Background(), "b"); len(got) != 0 {
		t.Errorf("got %d routes, want none", len(got))
	}
}

// Provisioned state has two homes and they must not be confused: the primary
// keeps using the servers row, exactly where every existing install already has
// it, and extras use their own row.
func TestProvisionedStateGoesToTheRightRow(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	seedRouteServer(t, s, "c", "notes", "")
	rid := uuid.New().String()
	s.db.Exec("INSERT INTO server_routes (id, server_id, hostname, port_name) VALUES (?,?,?,?)",
		rid, "c", "extra.example.com", "")

	routes := s.serverRoutes(ctx, "c")
	s.setRouteProvisionedCF(ctx, "c", routes[0], "notes.example.com")
	s.setRouteProvisionedCF(ctx, "c", routes[1], "extra.example.com")
	s.setRouteProvisionedNPM(ctx, "c", routes[0], 11)
	s.setRouteProvisionedNPM(ctx, "c", routes[1], 22)

	var srvHost string
	var srvNPM int
	s.db.QueryRow("SELECT cf_hostname, npm_host_id FROM servers WHERE id='c'").Scan(&srvHost, &srvNPM)
	if srvHost != "notes.example.com" || srvNPM != 11 {
		t.Errorf("servers row = %q/%d, want notes.example.com/11", srvHost, srvNPM)
	}
	var rHost string
	var rNPM int
	s.db.QueryRow("SELECT cf_hostname, npm_host_id FROM server_routes WHERE id=?", rid).Scan(&rHost, &rNPM)
	if rHost != "extra.example.com" || rNPM != 22 {
		t.Errorf("route row = %q/%d, want extra.example.com/22", rHost, rNPM)
	}

	// Teardown must see both, or a removed server leaves live DNS behind.
	hosts := s.cfProvisionedHosts(ctx, "c")
	if len(hosts) != 2 {
		t.Fatalf("provisioned hosts = %v, want both", hosts)
	}
}

// Teardown reads what was provisioned, not what is configured. They diverge the
// moment somebody edits a hostname, and removing the configured one would leave
// the live record orphaned.
func TestTeardownUsesProvisionedNotConfigured(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	seedRouteServer(t, s, "d", "renamed", "")
	s.db.Exec("UPDATE servers SET cf_hostname='old.example.com' WHERE id='d'")

	hosts := s.cfProvisionedHosts(ctx, "d")
	if len(hosts) != 1 || hosts[0] != "old.example.com" {
		t.Errorf("provisioned = %v, want [old.example.com] — the hostname actually created", hosts)
	}
}
