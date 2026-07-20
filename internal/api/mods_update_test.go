package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/modrinth"
)

// Updating a mod whose newest build has a different filename must install the new
// jar AND remove the old one — otherwise the server loads two versions of the same
// mod and crashes. This drives that rename-and-remove path.
func TestModUpdateRemovesOldWhenRenamed(t *testing.T) {
	s := testServer(t)
	id, dir := seedModServer(t, s)
	modsDir := filepath.Join(dir, "mods")
	os.MkdirAll(modsDir, 0o755)
	oldName := "sodium-fabric-0.5.8+mc1.20.1.jar"
	os.WriteFile(filepath.Join(modsDir, oldName), []byte("old-jar"), 0o644)

	// Stub: the old jar's hash resolves to project "sodium"; the latest build has a
	// new filename.
	oldR, oldF, oldL := modResolveVersion, modFetchFile, modLookupByHashes
	defer func() { modResolveVersion, modFetchFile, modLookupByHashes = oldR, oldF, oldL }()
	modLookupByHashes = func(_ context.Context, hashes []string) (map[string]modrinth.Version, error) {
		return map[string]modrinth.Version{hashes[0]: {ID: "old", ProjectID: "sodium"}}, nil
	}
	modResolveVersion = func(_ context.Context, _ string, _ []string, _ string) (*modrinth.Version, error) {
		return &modrinth.Version{ID: "new", Files: file("sodium-fabric-0.5.13+mc1.20.1.jar")}, nil
	}
	modFetchFile = func(_ context.Context, f modrinth.File) ([]byte, error) { return []byte(f.Filename), nil }

	// %2B is the encoded '+'; a raw '+' in a query string decodes to a space.
	r := adminReq(t, "POST", "/api/servers/"+id+"/mods/update?file=sodium-fabric-0.5.8%2Bmc1.20.1.jar", "", id)
	w := httptest.NewRecorder()
	s.handleModUpdate(w, r)
	if w.Code != 200 {
		t.Fatalf("update returned %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(modsDir, "sodium-fabric-0.5.13+mc1.20.1.jar")); err != nil {
		t.Errorf("new jar not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(modsDir, oldName)); !os.IsNotExist(err) {
		t.Errorf("old jar still on disk — a stale duplicate would crash the server")
	}
}

// A hand-installed jar (unknown to Modrinth) can't be auto-updated — say so
// instead of failing obscurely.
func TestModUpdateRefusesUnknownJar(t *testing.T) {
	s := testServer(t)
	id, dir := seedModServer(t, s)
	os.MkdirAll(filepath.Join(dir, "mods"), 0o755)
	os.WriteFile(filepath.Join(dir, "mods", "custom.jar"), []byte("x"), 0o644)

	oldL := modLookupByHashes
	defer func() { modLookupByHashes = oldL }()
	modLookupByHashes = func(_ context.Context, hashes []string) (map[string]modrinth.Version, error) {
		return map[string]modrinth.Version{}, nil // not found on Modrinth
	}

	r := adminReq(t, "POST", "/api/servers/"+id+"/mods/update?file=custom.jar", "", id)
	w := httptest.NewRecorder()
	s.handleModUpdate(w, r)
	if w.Code != 400 {
		t.Errorf("unknown jar update returned %d, want 400", w.Code)
	}
}
