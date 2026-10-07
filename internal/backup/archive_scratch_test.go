package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A browser tab closed mid-upload leaves a .part file of up to the whole modpack
// in the server's data directory. A backup that runs in the hours before the
// sweep collects it would carry it to the NAS — doubling the archive for bytes
// that are not data, and that nothing could restore.
func TestArchiveSkipsUploadScratch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "server.properties"), []byte("motd=hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, UploadScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	// Big enough that including it would be obvious in the numbers, small enough
	// to stay a unit test.
	if err := os.WriteFile(filepath.Join(dir, UploadScratchDir, "abc123.part"), bytes.Repeat([]byte("x"), 200_000), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	skipped, err := Archive(dir, nil, &buf)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("unexpected skipped entries: %v", skipped)
	}

	var names []string
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}

	var found bool
	for _, n := range names {
		if strings.Contains(n, UploadScratchDir) {
			t.Errorf("archive carries upload scratch: %s", n)
		}
		if n == "server.properties" {
			found = true
		}
	}
	// Print the number next to the green: an archive that excluded everything
	// would also contain no scratch.
	t.Logf("archive holds %d entries: %v", len(names), names)
	if !found {
		t.Fatal("archive does not contain server.properties — the exclusion took the real files with it")
	}
}
