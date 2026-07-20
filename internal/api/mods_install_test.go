package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/modrinth"
)

// installOne must pull in a mod's REQUIRED dependencies (a Fabric mod that needs
// Fabric API is broken without it), drop every jar in the server's folder, and
// walk a shared dependency once — not loop or install it twice.
func TestInstallResolvesRequiredDepsOnceEach(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()

	// Graph: mod-a → (required) lib-x, (required) mod-b; mod-b → (required) lib-x.
	// lib-x is shared (diamond) and must be installed exactly once.
	graph := map[string]modrinth.Version{
		"mod-a": {ID: "va", Files: file("mod-a.jar"), Dependencies: reqs("lib-x", "mod-b")},
		"mod-b": {ID: "vb", Files: file("mod-b.jar"), Dependencies: reqs("lib-x")},
		"lib-x": {ID: "vx", Files: file("lib-x.jar"), Dependencies: nil},
	}
	restore := stubModrinth(graph)
	defer restore()

	var installed []string
	err := s.installOne(context.Background(), dir, modProfile{Loaders: []string{"fabric"}, Folder: "mods"},
		"1.20.1", "mod-a", map[string]bool{}, &installed)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	// All three jars on disk, in mods/.
	for _, name := range []string{"mod-a.jar", "mod-b.jar", "lib-x.jar"} {
		if _, err := os.Stat(filepath.Join(dir, "mods", name)); err != nil {
			t.Errorf("%s not written to mods/: %v", name, err)
		}
	}
	// lib-x installed once despite two required edges.
	if n := count(installed, "lib-x.jar"); n != 1 {
		t.Errorf("lib-x.jar installed %d times, want exactly 1 (diamond dep)", n)
	}
	if len(installed) != 3 {
		t.Errorf("installed %v, want 3 distinct jars", installed)
	}
}

// Optional dependencies are the operator's choice — they must NOT be pulled in.
func TestInstallSkipsOptionalDeps(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	graph := map[string]modrinth.Version{
		"mod-a": {ID: "va", Files: file("mod-a.jar"), Dependencies: []modrinth.Dependency{
			{ProjectID: "opt-y", DependencyType: "optional"},
		}},
		"opt-y": {ID: "vy", Files: file("opt-y.jar")},
	}
	defer stubModrinth(graph)()

	var installed []string
	if err := s.installOne(context.Background(), dir, modProfile{Loaders: []string{"fabric"}, Folder: "mods"}, "1.20.1", "mod-a", map[string]bool{}, &installed); err != nil {
		t.Fatal(err)
	}
	if count(installed, "opt-y.jar") != 0 {
		t.Errorf("optional dependency was installed: %v", installed)
	}
	if _, err := os.Stat(filepath.Join(dir, "mods", "opt-y.jar")); !os.IsNotExist(err) {
		t.Errorf("optional dep jar exists on disk")
	}
}

// --- helpers ---

func file(name string) []modrinth.File {
	f := modrinth.File{Filename: name, URL: "https://cdn.modrinth.com/" + name, Primary: true}
	f.Hashes.SHA512 = "sha-" + name
	return []modrinth.File{f}
}

func reqs(ids ...string) []modrinth.Dependency {
	var d []modrinth.Dependency
	for _, id := range ids {
		d = append(d, modrinth.Dependency{ProjectID: id, DependencyType: "required"})
	}
	return d
}

// stubModrinth swaps the resolve/fetch indirections for an in-memory graph and
// returns a restore func. FetchFile returns the filename as bytes so the writer
// path runs without a real download.
func stubModrinth(graph map[string]modrinth.Version) func() {
	oldR, oldF := modResolveVersion, modFetchFile
	modResolveVersion = func(_ context.Context, id string, _ []string, _ string) (*modrinth.Version, error) {
		v, ok := graph[id]
		if !ok {
			return nil, modrinth.ErrNotFound
		}
		return &v, nil
	}
	modFetchFile = func(_ context.Context, f modrinth.File) ([]byte, error) {
		return []byte(f.Filename), nil
	}
	return func() { modResolveVersion, modFetchFile = oldR, oldF }
}

func count(xs []string, want string) int {
	n := 0
	for _, x := range xs {
		if x == want {
			n++
		}
	}
	return n
}
