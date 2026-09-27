package api

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jarWith writes a jar containing the named manifest entries. The cases below
// are the ones that actually happened on this fleet, reconstructed from the
// manifests that were read off the real files.
func jarWith(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z := zip.NewWriter(f)
	for n, body := range files {
		w, err := z.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

const fabricCreate = `{"id":"create","version":"6.0.9","environment":"*","depends":{"minecraft":"26.2","fabric-api":"*"}}`
const fabricClientOnly = `{"id":"create_cyber_goggles","version":"7.0.1","environment":"client","depends":{"minecraft":"*","create":"*"}}`

// Taken verbatim from real jars on this fleet, because the two of them disagree
// about whitespace and a pattern written from an idea of the format would pass
// on one and miss the other. waystones-forge-1.20.1 writes modId="minecraft";
// CreateCyberGoggles-1.20.1-Forge writes modId = "minecraft".
const forgeToml = `loaderVersion="[46,)"
modId="waystones"
[[dependencies.waystones]]
    modId="forge"
    versionRange="[46.0.0,)"
[[dependencies.waystones]]
    modId="minecraft"
    versionRange="[1.20,1.21)"
`

const forgeTomlSpaced = `loaderVersion = "0"
modId = "create_cyber_goggles"
[[dependencies."create_cyber_goggles"]]
modId = "forge"
versionRange = "0"
[[dependencies."create_cyber_goggles"]]
modId = "minecraft"
versionRange = "0"
`
const pluginYML = "name: CoreProtect\napi-version: '1.21'\nmain: net.coreprotect.CoreProtect\n"

func TestJudgeMod(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name       string
		jar        map[string]string
		serverType string
		mc         string
		wantOK     bool
		wantIn     string // substring the explanation must contain
	}{
		{"forge jar on a fabric server", map[string]string{"META-INF/mods.toml": forgeToml},
			"fabric", "26.2", false, "wrong loader"},
		{"fabric mod on a fabric server", map[string]string{"fabric.mod.json": fabricCreate},
			"fabric", "26.2", true, ""},
		{"client-only mod on a server", map[string]string{"fabric.mod.json": fabricClientOnly},
			"fabric", "26.2", false, "client"},
		{"a mod on Paper cannot exist", map[string]string{"fabric.mod.json": fabricCreate},
			"paper", "26.2", false, "Bukkit API"},
		{"a Bukkit plugin on Paper", map[string]string{"plugin.yml": pluginYML},
			"paper", "26.2", true, ""},
		{"a Bukkit plugin on Fabric", map[string]string{"plugin.yml": pluginYML},
			"fabric", "26.2", false, "mod loader cannot read"},
		{"vanilla loads nothing", map[string]string{"fabric.mod.json": fabricCreate},
			"vanilla", "26.2", false, "loads nothing"},
		{"a jar that is neither", map[string]string{"README.txt": "hello"},
			"fabric", "26.2", false, "not a mod or plugin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := jarWith(t, filepath.Join(dir, strings.ReplaceAll(c.name, " ", "_")), "x.jar", c.jar)
			m, err := inspectModJar(p)
			if err != nil {
				t.Fatalf("inspect: %v", err)
			}
			v := judgeMod(c.serverType, c.mc, m)
			if v.OK != c.wantOK {
				t.Fatalf("OK=%v want %v (problem=%q detail=%q)", v.OK, c.wantOK, v.Problem, v.Detail)
			}
			if c.wantIn != "" && !strings.Contains(v.Problem+" "+v.Detail, c.wantIn) {
				t.Errorf("explanation must say why: got %q / %q, want it to mention %q",
					v.Problem, v.Detail, c.wantIn)
			}
		})
	}
}

// A version RANGE is not interpreted. Claiming "incompatible" from a range the
// code cannot evaluate would be worse than saying nothing: the operator deletes
// a mod that worked.
func TestVersionRangeIsReportedNotJudged(t *testing.T) {
	dir := t.TempDir()
	p := jarWith(t, dir, "ranged.jar", map[string]string{"META-INF/mods.toml": forgeToml})
	m, _ := inspectModJar(p)
	v := judgeMod("forge", "26.2", m)
	if !v.OK {
		t.Fatalf("a range must not produce a verdict, got problem=%q detail=%q", v.Problem, v.Detail)
	}
	if !strings.Contains(v.Detail, "[1.20,1.21)") {
		t.Errorf("the declared range should still be shown as context, got %q", v.Detail)
	}

	// The other real spelling, with spaces around each "=".
	p2 := jarWith(t, dir, "spaced.jar", map[string]string{"META-INF/mods.toml": forgeTomlSpaced})
	m2, _ := inspectModJar(p2)
	if v2 := judgeMod("forge", "26.2", m2); !v2.OK {
		t.Errorf("versionRange \"0\" is not a version and must not be judged: %s — %s", v2.Problem, v2.Detail)
	}
}

// An exact version that differs IS decidable, and this is the case that cost an
// afternoon: create-1.20.1 in the mods/ of a 26.2 server.
func TestExactVersionMismatchIsCaught(t *testing.T) {
	dir := t.TempDir()
	p := jarWith(t, dir, "old.jar", map[string]string{
		"fabric.mod.json": `{"id":"create","version":"6.0.8","depends":{"minecraft":"1.20.1"}}`})
	m, _ := inspectModJar(p)
	v := judgeMod("fabric", "26.2", m)
	if v.OK {
		t.Fatal("a jar stating Minecraft 1.20.1 must not pass on a 26.2 server")
	}
	if !strings.Contains(v.Detail, "1.20.1") || !strings.Contains(v.Detail, "26.2") {
		t.Errorf("the message must name both versions, got %q", v.Detail)
	}
}

func TestMissingFabricAPIIsReportedOnce(t *testing.T) {
	data := t.TempDir()
	mods := filepath.Join(data, "mods")
	jarWith(t, mods, "a.jar", map[string]string{"fabric.mod.json": fabricCreate})
	jarWith(t, mods, "b.jar", map[string]string{
		"fabric.mod.json": `{"id":"copycats","version":"3","depends":{"minecraft":"26.2","fabric-api":"*"}}`})

	got, sub, err := checkServerMods(data, "fabric", "26.2")
	if err != nil {
		t.Fatal(err)
	}
	if sub != "mods" {
		t.Errorf("a fabric server's folder is mods/, got %q", sub)
	}
	n := 0
	for _, v := range got {
		if strings.Contains(v.File, "fabric-api") {
			n++
			if !strings.Contains(v.Detail, "a.jar") || !strings.Contains(v.Detail, "b.jar") {
				t.Errorf("the one report should name every dependant, got %q", v.Detail)
			}
		}
	}
	if n != 1 {
		t.Errorf("missing fabric-api reported %d times, want exactly 1", n)
	}

	// …and not reported at all once it is installed.
	jarWith(t, mods, "fabric-api-0.161.0.jar", map[string]string{
		"fabric.mod.json": `{"id":"fabric-api","version":"0.161.0","depends":{"minecraft":"26.2"}}`})
	got, _, _ = checkServerMods(data, "fabric", "26.2")
	for _, v := range got {
		if strings.Contains(v.File, "(missing)") {
			t.Errorf("fabric-api is installed, but it is still reported missing: %q", v.Detail)
		}
	}
}

// A Paper server's folder is plugins/, not mods/ — reading the wrong one would
// report an empty folder on a server full of plugins.
func TestPaperReadsPluginsFolder(t *testing.T) {
	data := t.TempDir()
	jarWith(t, filepath.Join(data, "plugins"), "coreprotect.jar", map[string]string{"plugin.yml": pluginYML})
	got, sub, err := checkServerMods(data, "paper", "26.2")
	if err != nil {
		t.Fatal(err)
	}
	if sub != "plugins" {
		t.Fatalf("folder = %q, want plugins", sub)
	}
	if len(got) != 1 || !got[0].OK {
		t.Errorf("a Bukkit plugin on Paper must pass, got %+v", got)
	}
}

// The bug this test exists for was found by the suite above and is worth its own
// name, because the comment explaining it is the kind that gets "simplified"
// later: a Bukkit plugin's api-version is the API level it was built against,
// not a Minecraft version it is limited to. Measured on this fleet — the Warden
// agent declares api-version 1.21 and loads cleanly on Paper 26.2. Judging it
// as a version mismatch tells the operator to delete a plugin that works.
func TestPluginAPIVersionIsNotAMinecraftVersion(t *testing.T) {
	dir := t.TempDir()
	p := jarWith(t, dir, "warden-agent.jar", map[string]string{
		"plugin.yml": "name: Warden\napi-version: '1.21'\nmain: dk.nolimit.warden.agent.WardenPlugin\n"})
	m, _ := inspectModJar(p)
	v := judgeMod("paper", "26.2", m)
	if !v.OK {
		t.Fatalf("api-version 1.21 must not be read as \"only Minecraft 1.21\": %s — %s", v.Problem, v.Detail)
	}
	if !strings.Contains(v.Detail, "API") {
		t.Errorf("the note should say it is an API level, not a game version: %q", v.Detail)
	}
	// …while a MOD stating an exact version still is judged.
	p2 := jarWith(t, dir, "mod.jar", map[string]string{
		"fabric.mod.json": `{"id":"x","version":"1","depends":{"minecraft":"1.20.1"}}`})
	m2, _ := inspectModJar(p2)
	if judgeMod("fabric", "26.2", m2).OK {
		t.Error("a mod declaring an exact, different Minecraft version must still be caught")
	}
}
