package api

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The regression this exists for: directory modes were hardcoded to 0755 on
// import, so a container user (PHP as www-data, a game as its own uid) could not
// write anywhere inside an imported server — while the same server worked on the
// panel it came from. Files kept their mode; directories did not, and that is the
// half that matters, because creating a file needs write permission on its
// directory.
func TestBundleRoundTripPreservesDirectoryModes(t *testing.T) {
	// A source data dir shaped like a real one: a widened top level, a widened
	// subdirectory the app writes into, and a tight one that must stay tight.
	src := t.TempDir()
	mkdir(t, filepath.Join(src, "uploads"), 0o777)
	mkdir(t, filepath.Join(src, "private"), 0o700)
	write(t, filepath.Join(src, "uploads", "a.txt"), 0o664)
	write(t, filepath.Join(src, "private", "secret.txt"), 0o600)

	// Export.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tarDataDir(tw, src, "data/")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	// Import, through the same helper importServerBundle uses.
	dst := t.TempDir()
	tr := tar.NewReader(&buf)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if rel := strings.TrimPrefix(hdr.Name, "data/"); rel != hdr.Name && rel != "" {
			extractBundleEntry(dst, rel, hdr, tr)
		}
	}

	for _, c := range []struct {
		path string
		want os.FileMode
	}{
		{"uploads", 0o777}, // the case that was broken
		{"private", 0o700}, // must not be widened
		{"uploads/a.txt", 0o664},
		{"private/secret.txt", 0o600},
	} {
		fi, err := os.Stat(filepath.Join(dst, c.path))
		if err != nil {
			t.Errorf("%s: %v", c.path, err)
			continue
		}
		if got := fi.Mode().Perm(); got != c.want {
			t.Errorf("%s: mode %04o, want %04o", c.path, got, c.want)
		}
	}
}

// A crafted entry must not be able to write outside the data dir.
func TestExtractBundleEntryStaysInsideDataDir(t *testing.T) {
	dst := t.TempDir()
	outside := filepath.Join(dst, "..", "escaped.txt")
	hdr := &tar.Header{Name: "data/../escaped.txt", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg}
	extractBundleEntry(dst, "../escaped.txt", hdr, strings.NewReader("bad"))
	if _, err := os.Stat(outside); err == nil {
		t.Error("entry escaped the data dir")
	}
	if _, err := os.Stat(filepath.Join(dst, "escaped.txt")); err != nil {
		t.Error("the entry should have been clamped into the data dir, not dropped")
	}
}

func mkdir(t *testing.T, p string, m os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(p, m); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, m); err != nil { // MkdirAll applies umask
		t.Fatal(err)
	}
}

func write(t *testing.T, p string, m os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), m); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, m); err != nil {
		t.Fatal(err)
	}
}
