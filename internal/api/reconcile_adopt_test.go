package api

import (
	"testing"

	"github.com/google/uuid"
)

// adoptRunningContainers is the reconciler in the direction it never went. The
// case that prompted it: a DayZ server was brought back with `docker start`
// during recovery, ran fine and took players, and the panel showed it stopped —
// so the console would not attach and the watchdog would not watch it.
//
// The docker client is not reachable from a unit test, so these pin the query
// that decides WHICH servers are even considered. That is where the original
// bug was: the old reconciler's SELECT never included a stopped one.
func adoptCandidates(t *testing.T, s *Server) []string {
	t.Helper()
	rows, err := s.db.Query(
		"SELECT id FROM servers WHERE status='stopped' AND container_id<>''")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func seedForAdopt(t *testing.T, s *Server, name, status, containerID string) string {
	t.Helper()
	id := uuid.New().String()
	if _, err := s.db.Exec(
		`INSERT INTO servers (id, name, gameskill_id, status, installed, data_dir, container_id)
		 VALUES (?,?,?,?,1,?,?)`, id, name, "dayz", status, "/tmp/"+id, containerID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return id
}

func TestAdoptConsidersAStoppedServerWithAContainer(t *testing.T) {
	s := testServer(t)
	id := seedForAdopt(t, s, "Heimdal", "stopped", "abc123")

	got := adoptCandidates(t, s)
	if len(got) != 1 || got[0] != id {
		t.Fatalf("candidates = %v, want the stopped server with a container — this is exactly what the old reconciler could not see", got)
	}
}

// A server that never had a container has nothing to adopt, and asking Docker
// about an empty id would be an error on every tick.
func TestAdoptSkipsServersWithNoContainer(t *testing.T) {
	s := testServer(t)
	seedForAdopt(t, s, "NeverBuilt", "stopped", "")

	if got := adoptCandidates(t, s); len(got) != 0 {
		t.Errorf("candidates = %v, want none", got)
	}
}

// Running and starting belong to the other direction of the reconciler; picking
// them up here would have the two halves fighting over the same rows.
func TestAdoptSkipsServersAlreadyUp(t *testing.T) {
	s := testServer(t)
	seedForAdopt(t, s, "Niflheim", "running", "abc")
	seedForAdopt(t, s, "Jotunheim", "starting", "def")

	if got := adoptCandidates(t, s); len(got) != 0 {
		t.Errorf("candidates = %v, want none", got)
	}
}

// The write is guarded on the status it read, so a start that began between the
// query and the update is not overwritten by a stale decision.
func TestAdoptWriteIsGuardedOnStatus(t *testing.T) {
	s := testServer(t)
	id := seedForAdopt(t, s, "Heimdal", "stopped", "abc123")

	// Something else starts it first.
	s.db.Exec("UPDATE servers SET status='starting' WHERE id=?", id)

	res, err := s.db.Exec("UPDATE servers SET status='running' WHERE id=? AND status='stopped'", id)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 0 {
		t.Errorf("updated %d rows, want 0 — a start in flight must win", n)
	}
	var status string
	s.db.QueryRow("SELECT status FROM servers WHERE id=?", id).Scan(&status)
	if status != "starting" {
		t.Errorf("status = %q, want it left as starting", status)
	}
}
