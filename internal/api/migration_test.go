package api

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const migrationTestRune = `
gameskill:
  id: mc-test
  name: MC Test
  category: game
  version: 1
  docker:
    image: nginx:alpine
  startup:
    command: "run"
  ports:
    - name: game
      default: 25565
      protocol: tcp
`

func seedMigrationServer(t *testing.T, s *Server, id, name string) string {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	os.MkdirAll(dataDir, 0o755)
	os.WriteFile(filepath.Join(dataDir, "world.txt"), []byte("blocks"), 0o644)
	s.db.Exec("INSERT OR IGNORE INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('mc-test','MC Test','game',1,?,0)", migrationTestRune)
	s.db.Exec(`INSERT INTO servers (id, name, gameskill_id, status, env_json, ports_json, data_dir)
		VALUES (?,?, 'mc-test','stopped','{}','{"game":25565}',?)`, id, name, dataDir)
	s.db.Exec("INSERT INTO schedules (id, name, server_id, cron_expr, action, args_json, enabled) VALUES (?,?,?,'0 4 * * *','restart','{}',1)",
		id+"-sched", "nightly", id)
	return dataDir
}

func TestMigrationArchiveRoundTrip(t *testing.T) {
	src := transferTestServer(t, "source-key-0123456789abcdef-xyz")
	seedMigrationServer(t, src, "srv-1", "Valhal")
	enc, _ := src.cipher.Encrypt(`{"webhook":"https://hook/x"}`)
	src.db.Exec("INSERT INTO notifications (id, type, config_enc, enabled, server_id) VALUES ('gn','discord',?,1,'')", enc)

	// Export the archive: settings (channels) + the one server.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/migration/export?include=channels&servers=srv-1", nil)
	src.handleMigrationExport(rec, req)
	archive := rec.Body.Bytes()
	if len(archive) == 0 {
		t.Fatal("empty archive")
	}

	// The archive must hold exactly panel.json + the named server bundle.
	gz, err := gzip.NewReader(strings.NewReader(rec.Body.String()))
	if err != nil {
		t.Fatalf("archive not gzip: %v", err)
	}
	var entries []string
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("archive walk: %v", err)
		}
		entries = append(entries, hdr.Name)
	}
	want := []string{"panel.json", "servers/Valhal/manifest.json", "servers/Valhal/data/world.txt"}
	if len(entries) != len(want) {
		t.Fatalf("unexpected archive entries: %v", entries)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Fatalf("entry %d: got %q want %q", i, entries[i], want[i])
		}
	}

	// Import on a target (different key) that already has a "Valhal" — with
	// skip_existing the server is reported skipped, the settings still merge.
	dst := transferTestServer(t, "target-key-fedcba9876543210-abc")
	seedMigrationServer(t, dst, "srv-9", "Valhal")
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/migration/import?skip_existing=1", strings.NewReader(string(archive)))
	dst.handleMigrationImport(rec2, req2)

	var resp struct {
		Panel   map[string]int   `json:"panel"`
		Servers []map[string]any `json:"servers"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("import response: %v (%s)", err, rec2.Body.String())
	}
	if resp.Panel["channels_added"] != 1 {
		t.Fatalf("settings did not merge: %v", resp.Panel)
	}
	if len(resp.Servers) != 1 || resp.Servers[0]["skipped"] != true {
		t.Fatalf("expected the duplicate server to be skipped: %v", resp.Servers)
	}
	// Idempotence: importing the same archive again adds nothing new.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("POST", "/api/migration/import?skip_existing=1", strings.NewReader(string(archive)))
	dst.handleMigrationImport(rec3, req3)
	var resp3 struct {
		Panel map[string]int `json:"panel"`
	}
	json.Unmarshal(rec3.Body.Bytes(), &resp3) //nolint:errcheck
	if resp3.Panel["channels_added"] != 0 || resp3.Panel["channels_skipped"] != 1 {
		t.Fatalf("second import not idempotent: %v", resp3.Panel)
	}
}

func TestMigrationExportRejectsBadInput(t *testing.T) {
	s := transferTestServer(t, "source-key-0123456789abcdef-xyz")
	rec := httptest.NewRecorder()
	s.handleMigrationExport(rec, httptest.NewRequest("GET", "/api/migration/export", nil))
	if rec.Code != 400 {
		t.Fatalf("empty selection should 400, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.handleMigrationExport(rec, httptest.NewRequest("GET", "/api/migration/export?servers=nope", nil))
	if rec.Code != 400 {
		t.Fatalf("unknown server id should 400, got %d", rec.Code)
	}
}
