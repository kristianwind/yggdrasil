package migrate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/db"
)

// A whole-panel move must carry directory modes across, not flatten them to
// 0755. Getting this wrong locked containers that run as their own uid out of
// their own data on the new host — and quietly loosened a 0700 directory on the
// way, so it was wrong in both directions.
func TestImportPreservesModes(t *testing.T) {
	dir := t.TempDir()
	srcDB := filepath.Join(dir, "src.db")
	database, err := db.Open(srcDB)
	if err != nil {
		t.Fatal(err)
	}

	dataDir := filepath.Join(dir, "servers", "srv-1")
	// A directory the app must write into, and one that must stay private.
	mk(t, filepath.Join(dataDir, "uploads"), 0o777)
	mk(t, filepath.Join(dataDir, "private"), 0o700)
	wf(t, filepath.Join(dataDir, "uploads", "a.txt"), 0o664)
	wf(t, filepath.Join(dataDir, "private", "k.txt"), 0o600)

	database.Exec("INSERT INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('mc','MC','g',1,'x',1)")
	if _, err := database.Exec(
		"INSERT INTO servers (id,name,gameskill_id,status,env_json,ports_json,data_dir) VALUES ('srv-1','Asgard','mc','stopped','{}','{}',?)",
		dataDir); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.Auth.SecretKey = "the-original-key"
	cfg.Database.Path = srcDB

	var buf bytes.Buffer
	if err := Export(cfg, database, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	database.Close()

	os.RemoveAll(dataDir)
	if _, err := Import(&buf, filepath.Join(dir, "dst.db")); err != nil {
		t.Fatalf("import: %v", err)
	}

	for _, c := range []struct {
		rel  string
		want os.FileMode
	}{
		{"uploads", 0o777}, // the case that was broken
		{"private", 0o700}, // must not be widened
		{"uploads/a.txt", 0o664},
		{"private/k.txt", 0o600},
	} {
		fi, err := os.Stat(filepath.Join(dataDir, c.rel))
		if err != nil {
			t.Errorf("%s: %v", c.rel, err)
			continue
		}
		if got := fi.Mode().Perm(); got != c.want {
			t.Errorf("%s: mode %04o, want %04o", c.rel, got, c.want)
		}
	}
}

func mk(t *testing.T, p string, m os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(p, m); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, m); err != nil { // MkdirAll is subject to umask
		t.Fatal(err)
	}
}

func wf(t *testing.T, p string, m os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), m); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, m); err != nil {
		t.Fatal(err)
	}
}
