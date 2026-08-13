package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func seedWithDir(t *testing.T, s *Server, name, dir string) string {
	t.Helper()
	id := uuid.New().String()
	if _, err := s.db.Exec(
		`INSERT INTO servers (id, name, gameskill_id, status, installed, data_dir)
		 VALUES (?,?,?,'running',1,?)`, id, name, "readarr", dir); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return id
}

func dataAccessAlerts(t *testing.T, s *Server, serverID string) int {
	t.Helper()
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE server_id=? AND key='data-access' AND paged=1`,
		serverID).Scan(&n)
	return n
}

// A directory the panel can write is the normal case and must stay silent —
// this loop runs over every installed server, so a false positive here is noise
// on the whole fleet.
func TestDataAccessQuietWhenWritable(t *testing.T) {
	s := testServer(t)
	id := seedWithDir(t, s, "Readarr", t.TempDir())

	s.checkDataAccess()
	if got := dataAccessAlerts(t, s, id); got != 0 {
		t.Errorf("raised %d alerts on a writable directory, want 0", got)
	}
}

// The real case: an image chowned the directory to its own PUID and the panel
// can no longer write there. Simulated with mode bits, since the test cannot
// chown to another uid without root — the check is a write probe precisely so
// that either cause is caught.
func TestDataAccessRaisesWhenNotWritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: a mode-only lockout cannot be simulated")
	}
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(dir, 0o555); err != nil { // r-xr-xr-x: readable, not writable
		t.Fatal(err)
	}
	s := testServer(t)
	id := seedWithDir(t, s, "Readarr", dir)

	s.checkDataAccess()
	if got := dataAccessAlerts(t, s, id); got != 1 {
		t.Fatalf("raised %d alerts on an unwritable directory, want 1", got)
	}

	// Daily, not once per hourly scan.
	for i := 0; i < 3; i++ {
		s.checkDataAccess()
	}
	if got := dataAccessAlerts(t, s, id); got != 1 {
		t.Errorf("raised %d after repeat scans, want 1", got)
	}
}

// A server whose directory has not been created yet is not a fault, and must
// not be reported as one.
func TestDataAccessIgnoresMissingDir(t *testing.T) {
	s := testServer(t)
	id := seedWithDir(t, s, "Ghost", filepath.Join(t.TempDir(), "never-created"))

	s.checkDataAccess()
	if got := dataAccessAlerts(t, s, id); got != 0 {
		t.Errorf("raised %d alerts for a missing directory, want 0", got)
	}
}

// The probe must not leave anything behind in the server's file area, where the
// user would see it in the Files tab.
func TestDataAccessProbeLeavesNothing(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	seedWithDir(t, s, "Clean", dir)

	s.checkDataAccess()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("probe left %d entries behind: %v", len(entries), entries)
	}
}
