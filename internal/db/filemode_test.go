package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenLeavesTheDatabaseUnreadableToOtherUsers(t *testing.T) {
	old := setUmask(0o022)
	defer setUmask(old)
	path := filepath.Join(t.TempDir(), "yggdrasil.db")
	// An install from before this change: the file already exists, world-readable.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec("INSERT INTO app_settings (key, value) VALUES ('k','v')"); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Stat(f)
		if err != nil {
			continue
		}
		seen++
		if fi.Mode().Perm()&0o007 != 0 {
			t.Errorf("%s is %o", filepath.Base(f), fi.Mode().Perm())
		}
	}
	if seen < 2 {
		t.Fatalf("only %d of the database's files exist — the WAL was not checked", seen)
	}
}
