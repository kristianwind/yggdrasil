package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func tarGz(t *testing.T, entries []tar.Header, body string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		h := h
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(body)) //nolint:errcheck
		}
	}
	tw.Close()
	gz.Close()
	return &buf
}

// The data directory is not emptied before a restore, and its contents belong to
// whoever runs code in the server's container. A directory in the backup that
// has since been replaced by a symlink must not carry the restore out with it.
func TestRestoreDoesNotWriteThroughAnExistingSymlinkedParent(t *testing.T) {
	outside := t.TempDir()
	dest := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dest, "plugins")); err != nil {
		t.Fatal(err)
	}
	arc := tarGz(t, []tar.Header{
		{Name: "plugins/config.yaml", Typeflag: tar.TypeReg, Mode: 0o644},
	}, "owned")
	err := Restore(arc, dest)
	if _, statErr := os.Stat(filepath.Join(outside, "config.yaml")); statErr == nil {
		t.Fatalf("restore wrote outside the destination through a symlinked parent (err=%v)", err)
	}
	if err == nil {
		t.Fatal("restore reported success for an entry it could not place inside the destination")
	}
}

// Each link passes the lexical check alone; together they resolve to the parent
// of the destination. Nothing may be written there.
func TestRestoreDoesNotEscapeThroughAChainOfLinksInTheArchive(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "srv")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	arc := tarGz(t, []tar.Header{
		{Name: "x/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "x/l1", Typeflag: tar.TypeSymlink, Linkname: ".."},
		{Name: "l2", Typeflag: tar.TypeSymlink, Linkname: "x/l1/.."},
		{Name: "l2/escaped.txt", Typeflag: tar.TypeReg, Mode: 0o644},
	}, "owned")
	err := Restore(arc, dest)
	if _, statErr := os.Stat(filepath.Join(parent, "escaped.txt")); statErr == nil {
		t.Fatalf("restore wrote into the parent of the destination (err=%v)", err)
	}
}

// The ordinary case still has to work, in-tree symlinks included.
func TestRestoreStillPlacesFilesAndInternalLinks(t *testing.T) {
	dest := t.TempDir()
	arc := tarGz(t, []tar.Header{
		{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "world/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "world/level.dat", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "current", Typeflag: tar.TypeSymlink, Linkname: "world"},
		{Name: "current/extra.dat", Typeflag: tar.TypeReg, Mode: 0o644},
	}, "data")
	if err := Restore(arc, dest); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"world/level.dat", "world/extra.dat"} {
		b, err := os.ReadFile(filepath.Join(dest, f))
		if err != nil || string(b) != "data" {
			t.Fatalf("%s: %q, %v", f, b, err)
		}
	}
}
