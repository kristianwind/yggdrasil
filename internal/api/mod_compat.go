package api

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Why this exists, and why the judgement is here rather than in a prompt.
//
// Every mod problem on this fleet so far has been the same two questions asked
// of a file nobody opened: is this jar for the loader this server runs, and is
// it for the Minecraft version this server runs. A server with the wrong jars
// does not complain — Fabric and Bukkit both ignore what they cannot read — so
// it starts cleanly, the mod is simply absent, and the operator concludes the
// server is broken.
//
// Real cases, all on one afternoon:
//   - create-1.20.1-6.0.8.jar and Steam_Rails-1.7.3+forge-mc1.20.1.jar sitting
//     in the mods/ of a Fabric 26.2 server: Forge jars for a Minecraft five
//     versions older. Neither can load; nothing said so.
//   - CreateCyberGoggles declaring "environment": "client" on a dedicated
//     server, where it is correctly and silently ignored.
//   - Create asked for on Paper, where it cannot exist: Paper loads plugins
//     against the Bukkit API, Create is a mod against Forge/Fabric.
//
// Each of those is decidable by reading the jar's own manifest. So the verdict
// is computed here, from the file, and Kvasir's job is to explain the result —
// not to guess it. A model asked "will this mod work" will answer plausibly
// whether or not it knows, and plausibly wrong is the failure mode that costs
// an evening.

// modFamily is what a piece of server software can load.
type modFamily string

const (
	familyPlugin modFamily = "plugin" // Bukkit API: Paper, Purpur, Spigot, Folia
	familyFabric modFamily = "fabric"
	familyForge  modFamily = "forge"
	familyNeo    modFamily = "neoforge"
	familyNone   modFamily = "none" // vanilla: loads neither
)

// familyFor maps a server's SERVER_TYPE to what it can load.
func familyFor(serverType string) modFamily {
	switch strings.ToLower(strings.TrimSpace(serverType)) {
	case "paper", "purpur", "spigot", "bukkit", "folia":
		return familyPlugin
	case "fabric", "quilt":
		return familyFabric
	case "forge":
		return familyForge
	case "neoforge":
		return familyNeo
	}
	return familyNone
}

// modInfo is what a jar says about itself. Everything here is read from the
// file; nothing is inferred from its name, because names lie — a jar called
// "…-Fabric.jar" was, in one of the cases above, a client-only mod.
type modInfo struct {
	File       string
	Families   []modFamily // every family whose manifest the jar carries
	ID         string
	Version    string
	MCDeclared string // the Minecraft version or range the jar states, verbatim
	ClientOnly bool
	Requires   []string // declared dependencies, fabric only
}

// inspectModJar reads one jar's manifests. A jar may carry several (a mod built
// for both Forge and NeoForge ships both TOMLs), so Families is a set.
func inspectModJar(path string) (modInfo, error) {
	m := modInfo{File: filepath.Base(path)}
	z, err := zip.OpenReader(path)
	if err != nil {
		return m, fmt.Errorf("not a readable jar: %w", err)
	}
	defer z.Close()
	have := map[string]*zip.File{}
	for _, f := range z.File {
		have[f.Name] = f
	}
	if f, ok := have["fabric.mod.json"]; ok {
		m.Families = append(m.Families, familyFabric)
		readFabricManifest(f, &m)
	}
	if _, ok := have["META-INF/mods.toml"]; ok {
		m.Families = append(m.Families, familyForge)
		if f := have["META-INF/mods.toml"]; m.MCDeclared == "" {
			readTomlMinecraft(f, &m)
		}
	}
	if _, ok := have["META-INF/neoforge.mods.toml"]; ok {
		m.Families = append(m.Families, familyNeo)
		if f := have["META-INF/neoforge.mods.toml"]; m.MCDeclared == "" {
			readTomlMinecraft(f, &m)
		}
	}
	if f, ok := have["plugin.yml"]; ok {
		m.Families = append(m.Families, familyPlugin)
		readPluginYML(f, &m)
	}
	if _, ok := have["paper-plugin.yml"]; ok && !hasFamily(m.Families, familyPlugin) {
		m.Families = append(m.Families, familyPlugin)
	}
	return m, nil
}

func hasFamily(fs []modFamily, want modFamily) bool {
	for _, f := range fs {
		if f == want {
			return true
		}
	}
	return false
}

func readZipFile(f *zip.File, limit int64) []byte {
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	b, _ := io.ReadAll(io.LimitReader(rc, limit))
	return b
}

func readFabricManifest(f *zip.File, m *modInfo) {
	var d struct {
		ID          string          `json:"id"`
		Version     string          `json:"version"`
		Environment string          `json:"environment"`
		Depends     json.RawMessage `json:"depends"`
	}
	if json.Unmarshal(readZipFile(f, 1<<20), &d) != nil {
		return
	}
	m.ID, m.Version = d.ID, d.Version
	m.ClientOnly = strings.EqualFold(d.Environment, "client")
	var deps map[string]json.RawMessage
	if json.Unmarshal(d.Depends, &deps) == nil {
		for k, v := range deps {
			m.Requires = append(m.Requires, k)
			if k == "minecraft" {
				m.MCDeclared = strings.Trim(string(v), `"`)
			}
		}
		sort.Strings(m.Requires)
	}
}

// mods.toml is TOML, and the panel has no TOML parser. Only one field is needed
// — the minecraft dependency's versionRange — so it is read with a pattern
// rather than by adding a dependency for four lines of use. When the pattern
// does not match, MCDeclared stays empty and the verdict says "not stated",
// which is the honest answer rather than a guessed one.
var tomlMCRange = regexp.MustCompile(`(?s)modId\s*=\s*"minecraft".*?versionRange\s*=\s*"([^"]+)"`)

func readTomlMinecraft(f *zip.File, m *modInfo) {
	if g := tomlMCRange.FindSubmatch(readZipFile(f, 1<<20)); g != nil {
		m.MCDeclared = string(g[1])
	}
}

func readPluginYML(f *zip.File, m *modInfo) {
	for _, line := range strings.Split(string(readZipFile(f, 1<<20)), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "name:") && m.ID == "":
			m.ID = strings.Trim(strings.TrimSpace(strings.TrimPrefix(t, "name:")), `"'`)
		case strings.HasPrefix(t, "api-version:"):
			m.MCDeclared = strings.Trim(strings.TrimSpace(strings.TrimPrefix(t, "api-version:")), `"'`)
		}
	}
}

// modVerdict is one finding about one file.
type modVerdict struct {
	File    string
	OK      bool
	Problem string // empty when OK
	Detail  string
}

// judgeMod decides whether one jar can load on this server. It reports a problem
// only where the manifest makes it decidable; version RANGES are deliberately
// not interpreted, because a wrong "incompatible" is worse than an honest
// "cannot tell from the file".
func judgeMod(serverType, mcVersion string, m modInfo) modVerdict {
	fam := familyFor(serverType)
	v := modVerdict{File: m.File, OK: true}

	if len(m.Families) == 0 {
		v.OK, v.Problem = false, "not a mod or plugin"
		v.Detail = "the jar carries no fabric.mod.json, mods.toml, neoforge.mods.toml or plugin.yml, so nothing will load it"
		return v
	}
	if fam == familyNone {
		v.OK, v.Problem = false, "this server loads nothing"
		v.Detail = fmt.Sprintf("SERVER_TYPE is %q — vanilla loads neither mods nor plugins", serverType)
		return v
	}
	// NeoForge still loads many Forge-era mods; Forge does not load NeoForge ones.
	loadable := hasFamily(m.Families, fam) ||
		(fam == familyNeo && hasFamily(m.Families, familyForge))
	if !loadable {
		v.OK, v.Problem = false, "wrong loader"
		v.Detail = fmt.Sprintf("the jar is for %s, this server is %s — %s",
			joinFamilies(m.Families), serverType, whyLoaderMatters(fam, m.Families))
		return v
	}
	if m.ClientOnly {
		v.OK, v.Problem = false, "client-side only"
		v.Detail = `the jar declares "environment": "client" — it belongs in the players' own Minecraft, and a dedicated server ignores it`
		return v
	}
	// An exactly-stated version that is not this one is decidable — for a MOD.
	// For a Bukkit plugin it is not: api-version is the API level the plugin was
	// built against, not a Minecraft version it is limited to, and Paper loads
	// older-api plugins on purpose. Measured rather than assumed: the Warden
	// agent declares api-version 1.21 and loads cleanly on Paper 26.2. Judging
	// it here would have told an operator to delete a plugin that works, which
	// is the exact failure this file exists to prevent.
	plugin := hasFamily(m.Families, familyPlugin) && fam == familyPlugin
	if !plugin && m.MCDeclared != "" && isExactVersion(m.MCDeclared) && m.MCDeclared != mcVersion {
		v.OK, v.Problem = false, "wrong Minecraft version"
		v.Detail = fmt.Sprintf("the jar states Minecraft %s, this server runs %s", m.MCDeclared, mcVersion)
		return v
	}
	if m.MCDeclared != "" {
		if plugin {
			v.Detail = fmt.Sprintf("built against Bukkit API %s; Paper runs it on %s", m.MCDeclared, mcVersion)
		} else {
			v.Detail = fmt.Sprintf("declares Minecraft %s; this server runs %s", m.MCDeclared, mcVersion)
		}
	}
	return v
}

var exactVer = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}$`)

func isExactVersion(s string) bool { return exactVer.MatchString(strings.TrimSpace(s)) }

func joinFamilies(fs []modFamily) string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = string(f)
	}
	return strings.Join(out, "/")
}

func whyLoaderMatters(server modFamily, jar []modFamily) string {
	if server == familyPlugin && (hasFamily(jar, familyFabric) || hasFamily(jar, familyForge) || hasFamily(jar, familyNeo)) {
		return "Paper-family servers load plugins against the Bukkit API; this is a mod against a different one, and no build of it can exist for Paper"
	}
	if server != familyPlugin && hasFamily(jar, familyPlugin) {
		return "this is a Bukkit plugin, and a mod loader cannot read one"
	}
	return "a loader reads only its own manifest"
}

// checkServerMods reads every jar in the server's mod or plugin folder and
// judges it. dir is the server's data directory.
func checkServerMods(dataDir, serverType, mcVersion string) ([]modVerdict, string, error) {
	sub := "mods"
	if familyFor(serverType) == familyPlugin {
		sub = "plugins"
	}
	dir := filepath.Join(dataDir, sub)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, sub, err
	}
	var out []modVerdict
	fabricAPI := false
	needsFabricAPI := []string{}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".jar") {
			continue
		}
		m, ierr := inspectModJar(filepath.Join(dir, e.Name()))
		if ierr != nil {
			out = append(out, modVerdict{File: e.Name(), OK: false,
				Problem: "unreadable", Detail: ierr.Error()})
			continue
		}
		if m.ID == "fabric" || strings.HasPrefix(m.ID, "fabric-api") || strings.HasPrefix(m.File, "fabric-api") {
			fabricAPI = true
		}
		for _, r := range m.Requires {
			if r == "fabric-api" {
				needsFabricAPI = append(needsFabricAPI, m.File)
			}
		}
		out = append(out, judgeMod(serverType, mcVersion, m))
	}
	// A Fabric mod that depends on the API fails at load with a message most
	// people read as "the mod is broken". Reported once, not per file.
	if !fabricAPI && len(needsFabricAPI) > 0 {
		out = append(out, modVerdict{
			File: "(missing) fabric-api", OK: false, Problem: "required dependency absent",
			Detail: fmt.Sprintf("%d mod(s) here depend on Fabric API and it is not installed: %s",
				len(needsFabricAPI), strings.Join(needsFabricAPI, ", ")),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OK != out[j].OK {
			return !out[i].OK // problems first
		}
		return out[i].File < out[j].File
	})
	return out, sub, nil
}
