package db

import (
	"path/filepath"
	"testing"
)

// A DSN parameter the driver does not recognise is ignored in silence, so a
// connection string can promise three things and deliver none. That is what
// happened: the DSN carried mattn/go-sqlite3's syntax against modernc's driver,
// and for as long as it stood journal_mode was `delete`, busy_timeout was 0 and
// foreign_keys was 0 — every ON DELETE in the schema inert, with nothing
// anywhere saying so.
//
// So the pragmas are read back from the connection rather than trusted to the
// string. This is the shape of check the original had none of.
func TestOpenAppliesItsPragmas(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	var journal string
	if err := d.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal — the DSN promised it and the driver ignored it", journal)
	}

	var busy int
	if err := d.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if busy != 5000 {
		t.Errorf("busy_timeout = %d, want 5000 — a second process gets SQLITE_BUSY instantly instead of waiting", busy)
	}
}

// Foreign keys are deliberately still off, and that is worth asserting rather
// than leaving as an accident: the day somebody turns them on it should be a
// decision with a migration behind it, and this test is where they will read
// why. Every ON DELETE in the schema has been inert since the beginning, so
// these databases hold rows a constraint would have forbidden; enforcing it
// against them can make deletes that have always worked start failing.
func TestForeignKeysAreKnowinglyOff(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	var fk int
	if err := d.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if fk != 0 {
		t.Errorf("foreign_keys = %d. If this was deliberate, the migration that counts and clears "+
			"orphaned rows has to land with it — see the comment in Open", fk)
	}
}
