package api

import (
	"testing"

	"github.com/google/uuid"
)

func downServer(t *testing.T, s *Server, name string) string {
	t.Helper()
	id := uuid.New().String()
	if _, err := s.db.Exec(
		`INSERT INTO servers (id, name, gameskill_id, status, installed, data_dir)
		 VALUES (?,?,?,'stopped',1,?)`, id, name, "dayz", "/tmp/"+id); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	return id
}

func crashedAgo(t *testing.T, s *Server, serverID string, hours, code int) {
	t.Helper()
	if _, err := s.db.Exec(
		`INSERT INTO server_crashes (server_id, exit_code, ts)
		 VALUES (?,?, datetime('now', ?))`, serverID, code, hoursAgo(hours)); err != nil {
		t.Fatalf("seed crash: %v", err)
	}
}

func hoursAgo(h int) string { return "-" + itoa(h) + " hours" }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func pagedStillDown(t *testing.T, s *Server, serverID string) int {
	t.Helper()
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE server_id=? AND key='still-down' AND paged=1`,
		serverID).Scan(&n)
	return n
}

// The case this exists for: Heimdal exited at 02:04 and was still stopped two
// days later, with the panel writing "server is stopped" into the schedule log
// every night and saying nothing.
func TestStillDownRaisesAfterTheGrace(t *testing.T) {
	s := testServer(t)
	id := downServer(t, s, "Heimdal")
	crashedAgo(t, s, id, 48, 137)

	s.checkStillDown()
	if got := pagedStillDown(t, s, id); got != 1 {
		t.Fatalf("paged %d times, want 1 — a server down for 48h must be raised", got)
	}
}

// A server that only just died is not news: a restart, an image pull or a host
// reboot all look like this for a few minutes.
func TestStillDownStaysQuietInsideTheGrace(t *testing.T) {
	s := testServer(t)
	id := downServer(t, s, "Niflheim")
	crashedAgo(t, s, id, 1, 137)

	s.checkStillDown()
	if got := pagedStillDown(t, s, id); got != 0 {
		t.Errorf("paged %d times, want 0 — one hour is inside the grace period", got)
	}
}

// The distinction that keeps this usable. Most stopped servers are stopped
// because somebody stopped them, and paging about those would drown the ones
// that fell over.
func TestStillDownIgnoresAnOperatorStop(t *testing.T) {
	s := testServer(t)
	id := downServer(t, s, "Jotunheim")
	crashedAgo(t, s, id, 48, 137)
	// Someone acted on it after the exit — a stop, a delete, anything.
	s.db.Exec(`INSERT INTO audit_log (id, ts, action, username, resource)
	           VALUES (?, datetime('now','-40 hours'), 'server.stop', 'admin', ?)`,
		uuid.New().String(), "server:"+id)

	s.checkStillDown()
	if got := pagedStillDown(t, s, id); got != 0 {
		t.Errorf("paged %d times, want 0 — an operator acted after the crash", got)
	}
}

// Daily, not on every 15-minute scan.
func TestStillDownRepeatsOnlyOnceADay(t *testing.T) {
	s := testServer(t)
	id := downServer(t, s, "Heimdal")
	crashedAgo(t, s, id, 48, 137)

	for i := 0; i < 5; i++ {
		s.checkStillDown()
	}
	if got := pagedStillDown(t, s, id); got != 1 {
		t.Errorf("paged %d times across five scans, want 1", got)
	}
}

// A crash is an incident by nature and must not be measured as traffic: it has
// no sources and no hits, so the traffic classifier would file it as routine
// and swallow it — the trap player anomalies already avoid.
func TestRaiseIncidentIsAlwaysAnIncident(t *testing.T) {
	s := testServer(t)
	id := downServer(t, s, "Behrens1")

	if !s.raiseIncident(id, "crash", "Behrens1 exited unexpectedly (code 137)", "oom", alertPageCooldown) {
		t.Fatal("first crash must page")
	}
	var class string
	s.db.QueryRow(`SELECT class FROM alerts WHERE server_id=? AND key='crash'`, id).Scan(&class)
	if class != string(alertIncident) {
		t.Errorf("class = %q, want incident", class)
	}
	// And the dedupe is the same one every other detection gets.
	if s.raiseIncident(id, "crash", "again", "oom", alertPageCooldown) {
		t.Error("a second crash inside the cooldown must not page again")
	}
}
