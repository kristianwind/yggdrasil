package api

import (
	"os"
	"path/filepath"
	"testing"
)

// SERVER_TYPE "curseforge" names a shop, not a mod loader. Everything that asks
// what a server can load has to end up at the loader the pack actually brought,
// or a NeoForge modpack server shows no Mods tab, the compatibility check calls
// every jar in mods/ unloadable, and Kvasir says the server runs "curseforge".
func TestCurseForgeServerResolvesLoaderFromInstall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".ygg_loader"), []byte("neoforge 1.21.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := &serverRuntime{
		env:     map[string]string{"SERVER_TYPE": "curseforge", "MC_VERSION": "latest"},
		dataDir: dir,
	}

	st, mc := rt.mcTarget()
	if st != "neoforge" || mc != "1.21.1" {
		t.Fatalf("mcTarget() = %q, %q; want neoforge, 1.21.1", st, mc)
	}
	p, ok := modProfileFor(rt.modServerType())
	if !ok {
		t.Fatal("no mod profile — the Mods tab would be hidden on a modpack server")
	}
	if p.Folder != "mods" || p.Loaders[0] != "neoforge" {
		t.Errorf("profile = %+v, want neoforge mods/", p)
	}
	// MC_VERSION on the form is still "latest"; the pack's version is what a mod
	// search has to be filtered by, or it offers mods for the wrong Minecraft.
	if got := rt.modGameVersion(); got != "1.21.1" {
		t.Errorf("modGameVersion() = %q, want 1.21.1", got)
	}
}

// The marker is only consulted for curseforge. A Paper server whose data dir
// happens to hold one (restored from a backup of a modpack server, say) must
// still be Paper, because that is what its jar is.
func TestNonCurseForgeIgnoresLoaderMarker(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".ygg_loader"), []byte("neoforge 1.21.1\n"), 0o644) //nolint:errcheck
	rt := &serverRuntime{
		env:     map[string]string{"SERVER_TYPE": "paper", "MC_VERSION": "1.20.4"},
		dataDir: dir,
	}
	if st, mc := rt.mcTarget(); st != "paper" || mc != "1.20.4" {
		t.Errorf("mcTarget() = %q, %q; want paper, 1.20.4", st, mc)
	}
}

// The install writes "unknown" for a field it could not work out — a Fabric pack
// uploaded by hand carries its Minecraft version nowhere. Treating that as a
// value filters a mod search down to nothing and presents the empty result as an
// answer, so it has to read as absent instead.
func TestLoaderMarkerUnknownReadsAsAbsent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".ygg_loader"), []byte("fabric unknown\n"), 0o644) //nolint:errcheck
	rt := &serverRuntime{
		env:     map[string]string{"SERVER_TYPE": "curseforge", "MC_VERSION": "latest"},
		dataDir: dir,
	}
	st, mc := rt.mcTarget()
	if st != "fabric" {
		t.Errorf("mcTarget() server type = %q, want fabric", st)
	}
	if mc != "latest" {
		t.Errorf("mcTarget() version = %q, want the form's latest rather than %q", mc, "unknown")
	}
	if got := rt.modGameVersion(); got != "" {
		t.Errorf("modGameVersion() = %q, want empty so search stays unfiltered", got)
	}
}

// Before the first install there is no marker. Nothing may panic, and the Mods
// tab stays off until the pack has told us what it is.
func TestCurseForgeWithoutMarkerIsNotAModLoader(t *testing.T) {
	rt := &serverRuntime{
		env:     map[string]string{"SERVER_TYPE": "curseforge", "MC_VERSION": "latest"},
		dataDir: t.TempDir(),
	}
	if st, _ := rt.mcTarget(); st != "curseforge" {
		t.Errorf("mcTarget() = %q, want curseforge unchanged", st)
	}
	if _, ok := modProfileFor(rt.modServerType()); ok {
		t.Error("a pack that has not been installed yet reports a mod loader")
	}
	if l, v := readLoaderMarker(""); l != "" || v != "" {
		t.Errorf("readLoaderMarker(\"\") = %q, %q; want empty", l, v)
	}
}
