package api

import "testing"

// The SERVER_TYPE → Modrinth mapping decides where a jar lands and what shows up
// in search. A Fabric server must never be offered Paper plugins (they won't
// load), and vanilla has no mod folder at all.
func TestModProfileFor(t *testing.T) {
	cases := []struct {
		serverType string
		ok         bool
		folder     string
		hasLoader  string // a loader that must be in the search set
	}{
		{"fabric", true, "mods", "fabric"},
		{"forge", true, "mods", "forge"},
		{"paper", true, "plugins", "paper"},
		{"purpur", true, "plugins", "purpur"},
		{"PAPER", true, "plugins", "paper"}, // case-insensitive
		{"vanilla", false, "", ""},
		{"", false, "", ""},
		{"somegame", false, "", ""},
	}
	for _, c := range cases {
		p, ok := modProfileFor(c.serverType)
		if ok != c.ok {
			t.Errorf("%q: ok=%v want %v", c.serverType, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if p.Folder != c.folder {
			t.Errorf("%q: folder=%q want %q", c.serverType, p.Folder, c.folder)
		}
		found := false
		for _, l := range p.Loaders {
			if l == c.hasLoader {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: search loaders %v missing %q", c.serverType, p.Loaders, c.hasLoader)
		}
	}
}

// A Paper server loads Spigot and Bukkit plugins too, so search must include the
// whole family or half of Modrinth's plugins would be hidden.
func TestPaperSearchesPluginFamily(t *testing.T) {
	p, _ := modProfileFor("paper")
	for _, want := range []string{"paper", "spigot", "bukkit"} {
		found := false
		for _, l := range p.Loaders {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("paper search set %v missing %q", p.Loaders, want)
		}
	}
}

// "latest" or blank MC_VERSION can't be pinned, so it must resolve to "" (search
// any version) rather than the literal "latest", which matches no Modrinth build.
func TestModGameVersion(t *testing.T) {
	for _, v := range []string{"latest", "LATEST", "", "  "} {
		if got := modGameVersion(map[string]string{"MC_VERSION": v}); got != "" {
			t.Errorf("MC_VERSION=%q → %q, want empty", v, got)
		}
	}
	if got := modGameVersion(map[string]string{"MC_VERSION": "1.20.1"}); got != "1.20.1" {
		t.Errorf("concrete version = %q, want 1.20.1", got)
	}
}
