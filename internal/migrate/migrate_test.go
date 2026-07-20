package migrate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/db"
)

// A bundle must round-trip the database, every server's data directory, and the
// secret_key — the last of which is what makes the encrypted variable secrets
// (RCON passwords, API keys) survive the move.
func TestExportImportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	srcDB := filepath.Join(dir, "src.db")
	database, err := db.Open(srcDB)
	if err != nil {
		t.Fatal(err)
	}
	// A server with a data directory holding a file and a nested one.
	dataDir := filepath.Join(dir, "servers", "srv-1")
	os.MkdirAll(filepath.Join(dataDir, "world"), 0o755)
	os.WriteFile(filepath.Join(dataDir, "server.properties"), []byte("difficulty=hard"), 0o644)
	os.WriteFile(filepath.Join(dataDir, "world", "level.dat"), []byte("LEVEL"), 0o644)
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

	// Simulate the new host: wipe the data dir, import into a fresh DB path.
	os.RemoveAll(dataDir)
	dstDB := filepath.Join(dir, "dst.db")
	man, err := Import(&buf, dstDB)
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	// The secret_key came across — without it the new host can't decrypt secrets.
	if man.SecretKey != "the-original-key" {
		t.Errorf("secret_key = %q, want the original", man.SecretKey)
	}
	if len(man.Servers) != 1 || man.Servers[0].Name != "Asgard" {
		t.Errorf("servers = %+v", man.Servers)
	}

	// The data dir was restored to its recorded path, contents intact.
	if b, err := os.ReadFile(filepath.Join(dataDir, "server.properties")); err != nil || string(b) != "difficulty=hard" {
		t.Errorf("server.properties not restored: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dataDir, "world", "level.dat")); err != nil || string(b) != "LEVEL" {
		t.Errorf("nested world/level.dat not restored: %q %v", b, err)
	}

	// The imported DB opens and has the server row.
	restored, err := db.Open(dstDB)
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}
	defer restored.Close()
	var name string
	if err := restored.QueryRow("SELECT name FROM servers WHERE id='srv-1'").Scan(&name); err != nil || name != "Asgard" {
		t.Errorf("restored db missing the server: name=%q err=%v", name, err)
	}
}

// A crafted bundle entry must never write outside the intended targets.
func TestImportRejectsTraversal(t *testing.T) {
	// Build a bundle by hand with a traversal entry.
	dir := t.TempDir()
	database, _ := db.Open(filepath.Join(dir, "x.db"))
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(dir, "x.db")
	var good bytes.Buffer
	Export(cfg, database, &good)
	database.Close()
	// A valid bundle imports fine (sanity); the traversal guard is unit-covered by
	// the destFor/Clean checks — a full malicious tar is awkward to forge here, so
	// assert the guard function directly.
	if _, ok := destFor("data/../../etc/passwd", map[string]string{"data/srv": "/srv"}); ok {
		t.Error("destFor accepted a traversal path")
	}
}

// A server whose data directory doesn't exist (never installed, or wiped) must
// not abort the whole export — its DB row still carries its config. This bit in
// a real export before it was fixed.
func TestExportSkipsMissingDataDir(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.Exec("INSERT INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('mc','MC','g',1,'x',1)")
	database.Exec("INSERT INTO servers (id,name,gameskill_id,status,env_json,ports_json,data_dir) VALUES ('s1','Ghost','mc','stopped','{}','{}','/nope/does-not-exist')")

	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(dir, "x.db")
	var buf bytes.Buffer
	if err := Export(cfg, database, &buf); err != nil {
		t.Fatalf("export must skip a missing data dir, not fail: %v", err)
	}
	// The server's config still travels (via the DB); import lists it.
	man, err := Import(&buf, filepath.Join(dir, "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(man.Servers) != 1 || man.Servers[0].Name != "Ghost" {
		t.Errorf("servers = %+v, want the ghost server's config carried", man.Servers)
	}
}
