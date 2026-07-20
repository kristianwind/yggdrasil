package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

// seedModServer creates a Fabric Minecraft server whose data dir is a temp dir,
// and returns the id and dir.
func seedModServer(t *testing.T, s *Server) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := s.db.Exec(
		"INSERT INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('mc','MC','Minecraft',1,?,1)",
		"gameskill:\n  id: mc\n  name: MC\n  docker: { image: x }\n  startup: { command: run }\n"); err != nil {
		t.Fatal(err)
	}
	id := uuid.New().String()
	if _, err := s.db.Exec(
		"INSERT INTO servers (id,name,gameskill_id,status,env_json,ports_json,data_dir) VALUES (?,?,?,'stopped',?,'{}',?)",
		id, "modtest", "mc", `{"SERVER_TYPE":"fabric","MC_VERSION":"1.20.1"}`, dir); err != nil {
		t.Fatal(err)
	}
	return id, dir
}

// A mod jar's name contains '+' and '.', which a path segment mangles (chi leaves
// %2B encoded) — the reason remove takes the filename as a query param. This
// pins that: a real "+" filename must actually delete.
func TestModRemoveByQueryParamHandlesPlus(t *testing.T) {
	s := testServer(t)
	id, dir := seedModServer(t, s)
	if err := os.MkdirAll(filepath.Join(dir, "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := "sodium-fabric-0.5.13+mc1.20.1.jar"
	if err := os.WriteFile(filepath.Join(dir, "mods", name), []byte("jar"), 0o644); err != nil {
		t.Fatal(err)
	}

	// %2B is the encoded '+'; Query().Get decodes it to '+'.
	r := adminReq(t, "DELETE", "/api/servers/"+id+"/mods?file=sodium-fabric-0.5.13%2Bmc1.20.1.jar", "", id)
	w := httptest.NewRecorder()
	s.handleModRemove(w, r)
	if w.Code != 200 {
		t.Fatalf("remove returned %d, want 200: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "mods", name)); !os.IsNotExist(err) {
		t.Errorf("jar still on disk after remove")
	}
}

// Only .jar names are accepted; a traversal or non-jar is refused before any
// filesystem touch.
func TestModRemoveRejectsNonJarAndTraversal(t *testing.T) {
	s := testServer(t)
	id, _ := seedModServer(t, s)
	for _, bad := range []string{"", "notes.txt", "..%2F..%2Fserver.properties"} {
		r := adminReq(t, "DELETE", "/api/servers/"+id+"/mods?file="+bad, "", id)
		w := httptest.NewRecorder()
		s.handleModRemove(w, r)
		if w.Code != 400 {
			t.Errorf("file=%q returned %d, want 400", bad, w.Code)
		}
	}
}
