package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWipePathsJail(t *testing.T) {
	root := t.TempDir()
	must := func(e error) { if e != nil { t.Fatal(e) } }
	must(os.MkdirAll(filepath.Join(root, "world"), 0o755))
	must(os.WriteFile(filepath.Join(root, "world", "level.dat"), []byte("x"), 0o644))
	must(os.MkdirAll(filepath.Join(root, "mpmissions", "cher", "storage_1"), 0o755))
	must(os.WriteFile(filepath.Join(root, "keepme.cfg"), []byte("x"), 0o644))
	// A sibling dir the wipe must never reach via traversal.
	outside := filepath.Join(filepath.Dir(root), "OUTSIDE")
	must(os.MkdirAll(outside, 0o755))
	defer os.RemoveAll(outside)
	must(os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o644))

	s := &Server{}
	if _, err := s.wipePaths(root, []string{"world", "mpmissions/*/storage_*", "../OUTSIDE", "."}); err != nil {
		t.Fatal(err)
	}
	gone := func(p string) bool { _, e := os.Stat(p); return os.IsNotExist(e) }
	if !gone(filepath.Join(root, "world")) {
		t.Fatal("world not deleted")
	}
	if !gone(filepath.Join(root, "mpmissions", "cher", "storage_1")) {
		t.Fatal("storage not deleted")
	}
	if gone(filepath.Join(root, "keepme.cfg")) {
		t.Fatal("unmatched file was deleted")
	}
	if gone(filepath.Join(outside, "secret")) {
		t.Fatal("TRAVERSAL: a path outside the data dir was deleted")
	}
}
