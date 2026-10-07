package gameskill

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func loadMinecraftJava(t *testing.T) *Gameskill {
	t.Helper()
	b, err := os.ReadFile("../../builtin-runes/minecraft-java.yaml")
	if err != nil {
		t.Fatal(err)
	}
	gs, err := Parse(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return gs
}

func mcVar(gs *Gameskill, key string) *Variable {
	for i := range gs.Variables {
		if gs.Variables[i].Key == key {
			return &gs.Variables[i]
		}
	}
	return nil
}

// A CurseForge pack needs somewhere to say which pack, and CurseForge serves
// nothing anonymously — so a key field is not optional polish, it is the whole
// feature. The two key-free routes have to stay on the form as well: a pack
// whose author switched third-party downloads off cannot be fetched through the
// API by anyone, and a hand-downloaded zip is then the only way in.
func TestMinecraftJavaOffersCurseForge(t *testing.T) {
	gs := loadMinecraftJava(t)

	st := mcVar(gs, "SERVER_TYPE")
	if st == nil {
		t.Fatal("no SERVER_TYPE variable")
	}
	var found bool
	for _, o := range st.Options {
		if o == "curseforge" {
			found = true
		}
	}
	if !found {
		t.Errorf("SERVER_TYPE options = %v, want one of them to be curseforge", st.Options)
	}

	for _, key := range []string{"CF_MODPACK", "CF_FILE_ID", "CF_API_KEY", "CF_SERVERPACK_URL"} {
		if mcVar(gs, key) == nil {
			t.Errorf("no %s variable — the curseforge install reads it", key)
		}
	}
	if k := mcVar(gs, "CF_API_KEY"); k != nil && !k.Secret {
		t.Error("CF_API_KEY is not marked secret; it would render as plain text and be returned unmasked")
	}

	if !strings.Contains(gs.Install.Script, "curseforge)") {
		t.Error("install script has no curseforge branch, so the option would fail with 'unknown SERVER_TYPE'")
	}
}

// Which Java a Minecraft server needs is decided by its version, and a modpack
// is where that stops being the panel's choice: Minecraft 1.12 — RLCraft,
// SkyFactory, the older All the Mods — will not start on anything past 11.
func TestMinecraftJavaOffersOldRuntimes(t *testing.T) {
	gs := loadMinecraftJava(t)
	jv := mcVar(gs, "JAVA_VERSION")
	if jv == nil {
		t.Fatal("no JAVA_VERSION variable")
	}
	have := map[string]bool{}
	for _, o := range jv.Options {
		have[o] = true
	}
	for _, want := range []string{"8", "17", "21"} {
		if !have[want] {
			t.Errorf("JAVA_VERSION options = %v, want %s among them", jv.Options, want)
		}
	}
}

// Measured, not assumed: `java --enable-native-access=ALL-UNNAMED -version`
// exits 1 on eclipse-temurin:8-jre and :11-jre with "Unrecognized option" —
// an old JVM refuses to start on an option it does not know rather than
// ignoring it. The flag only silences a Java 24+ warning, so passing it
// unconditionally is how adding Java 8 to the list above would have made every
// old modpack unbootable, with the server dying before it logged a line.
func TestMinecraftJavaDoesNotForceNativeAccessFlag(t *testing.T) {
	gs := loadMinecraftJava(t)
	cmd := gs.Startup.Command

	for _, line := range strings.Split(cmd, "\n") {
		l := strings.TrimSpace(line)
		if !strings.HasPrefix(l, "exec java") {
			continue
		}
		if strings.Contains(l, "--enable-native-access") {
			t.Errorf("startup launches java with the flag hardcoded, so Java 8 and 11 — both selectable runtimes — refuse to start: %s", l)
		}
	}
	if !strings.Contains(cmd, "java --enable-native-access=ALL-UNNAMED -version") {
		t.Error("startup never asks the JVM whether it knows --enable-native-access; the flag is then either always on (breaks Java 8/11) or always off")
	}
	if !strings.Contains(cmd, "$NATIVE") {
		t.Error("startup does not pass the probed $NATIVE to java, so the probe decides nothing")
	}
}

// The RCON password is the one value in server.properties a person types, and
// the old re-stamp fed it to `sed s/^key=.*/key=VALUE/`, where a / ends the
// expression and an & means "the whole match". Run the rune's own setprop and
// check the file, rather than reading the code and agreeing with it.
func TestMinecraftJavaSetpropWritesValueLiterally(t *testing.T) {
	gs := loadMinecraftJava(t)

	var fn string
	for _, line := range strings.Split(gs.Startup.Command, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "setprop() {") {
			fn = strings.TrimSpace(line)
		}
	}
	if fn == "" {
		t.Fatal("no setprop() definition in the startup command")
	}

	dir := t.TempDir()
	const awkward = `p/w&x$1\n`
	if err := os.WriteFile(filepath.Join(dir, "server.properties"),
		[]byte("motd=Pack MOTD\nrcon.password=old\nlevel-type=biomesoplenty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := fn + "\nsetprop rcon.password \"$1\"\nsetprop enable-rcon true\n"
	cmd := exec.Command("/bin/sh", "-c", script, "sh", awkward)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setprop failed: %v\n%s", err, out)
	}

	got, err := os.ReadFile(filepath.Join(dir, "server.properties"))
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]string{}
	for _, l := range strings.Split(string(got), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			lines[k] = v
		}
	}
	if lines["rcon.password"] != awkward {
		t.Errorf("rcon.password = %q, want %q — the server would start with a password the panel does not have", lines["rcon.password"], awkward)
	}
	if lines["enable-rcon"] != "true" {
		t.Errorf("enable-rcon = %q, want true", lines["enable-rcon"])
	}
	// Everything the pack set and we did not touch has to survive, or a
	// reinstall silently regenerates the world under a different generator.
	if lines["motd"] != "Pack MOTD" || lines["level-type"] != "biomesoplenty" {
		t.Errorf("setprop lost the pack's own settings: %v", lines)
	}
	if strings.Count(string(got), "rcon.password=") != 1 {
		t.Errorf("rcon.password written more than once:\n%s", got)
	}
	if n := strings.Count(string(got), "\n\n"); n != 0 {
		t.Errorf("setprop left a blank line in server.properties:\n%q", got)
	}
}

// RCON is how the panel reaches the server at all — the console, the player
// list, scheduled commands and bans. A CurseForge pack ships its own
// server.properties with enable-rcon=false, and the install deliberately leaves
// a pack's file alone, so without this re-stamp every modpack server would come
// up with the panel unable to talk to it.
func TestMinecraftJavaReStampsRCON(t *testing.T) {
	gs := loadMinecraftJava(t)
	for _, want := range []string{"setprop enable-rcon true", `setprop rcon.port "$RCON_PORT"`, `setprop rcon.password "$RCON_PASSWORD"`} {
		if !strings.Contains(gs.Startup.Command, want) {
			t.Errorf("startup does not re-stamp RCON: %q missing", want)
		}
	}
}

// The install script is a 300-line shell program that only ever runs inside a
// container somebody is waiting on. A syntax error in a branch nobody took
// locally is invisible until an admin picks that server type.
func TestMinecraftJavaScriptsParseAsShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	gs := loadMinecraftJava(t)
	// {{VAR}} is substituted before the script ever reaches a shell; replace it
	// with a plain word here so the parse is of the shape, not of the braces.
	tpl := regexp.MustCompile(`\{\{[A-Za-z0-9_]+\}\}`)
	for name, script := range map[string]string{
		"install": gs.Install.Script,
		"startup": gs.Startup.Command,
	} {
		f := filepath.Join(t.TempDir(), "s.sh")
		if err := os.WriteFile(f, []byte(tpl.ReplaceAllString(script, "VALUE")), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
			t.Errorf("%s script is not valid shell: %v\n%s", name, err, out)
		}
	}
}

// A CurseForge CLIENT export is what somebody sends you when they built the pack
// themselves: manifest.json naming the loader and every mod by id, modlist.html,
// and overrides/. It contains no mods and no server, so without a branch for it
// the unpack produces three files and an error listing them — accurate, and no
// help at all.
func TestMinecraftJavaInstallsAClientManifest(t *testing.T) {
	gs := loadMinecraftJava(t)
	script := gs.Install.Script

	for _, want := range []string{"minecraftModpack", "manifest.json", "overrides"} {
		if !strings.Contains(script, want) {
			t.Errorf("install script never mentions %q, so a client export cannot be installed", want)
		}
	}

	// The key is demanded BEFORE a loader is installed. Getting this the other way
	// round costs a minute of somebody's time installing NeoForge for a pack that
	// then cannot be finished — the same error, paid for.
	//
	// Measured inside the client-pack block, not across the whole script: the
	// ordinary neoforge SERVER_TYPE branch downloads an installer by the same
	// name and sits earlier in the file, so a search over the whole thing answers
	// a different question correctly and reports this one as broken. It did, the
	// first time this test ran.
	block := script[strings.Index(script, "minecraftModpack"):]
	// The call has to be a call, not merely a line containing those words. A
	// `: ` in front of it — the shell no-op — leaves every substring in place and
	// switches the check off, and the first version of this test passed over
	// exactly that. Recognising a string is not evaluating what it does.
	key := lineStarting(block, `cf_need_key "This is a CurseForge client pack`)
	loader := lineStarting(block, "curl -fsSL -o neoforge-installer.jar")
	if key < 0 || loader < 0 {
		t.Fatalf("client-pack branch is not shaped as expected (key at %d, loader at %d)", key, loader)
	}
	if key > loader {
		t.Error("the client pack installs a loader before checking for an API key it cannot finish without")
	}

	// A pack lists resource packs and shaders beside its mods, and loading a
	// resource pack as a mod is a crash on start. Assert the routing itself, not
	// that the words appear: "mkdir -p mods resourcepacks shaderpacks" contains
	// both names while sending every file to mods/, which is how the loose
	// version of this check passed over a deleted case arm.
	for _, want := range []string{"12)   DESTDIR=resourcepacks", "6552) DESTDIR=shaderpacks", `case "$CLASS" in`} {
		if !strings.Contains(block, want) {
			t.Errorf("pack files are not sorted by kind: %q missing", want)
		}
	}
}

// lineStarting returns the offset of the first line whose trimmed text starts
// with prefix, or -1. Used instead of strings.Index where a disabled copy of the
// same line would otherwise read as the live one.
func lineStarting(s, prefix string) int {
	off := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return off
		}
		off += len(line) + 1
	}
	return -1
}

// Measured on bash and dash: a `while read` on the right-hand side of a pipe runs
// in a subshell, so every counter it keeps is discarded when the loop ends — the
// install would report "Installed 0 of 30 files" after installing all thirty, and
// the loop itself would look perfectly correct. The redirect form keeps the loop
// in the current shell.
func TestMinecraftJavaCountsDownloadsInTheCurrentShell(t *testing.T) {
	script := loadMinecraftJava(t).Install.Script
	if !strings.Contains(script, "done < .ygg_cf_files") {
		t.Error("the mod download loop does not read from a redirect; piping into `while read` loses every count it keeps")
	}
	for _, line := range strings.Split(script, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "#") {
			continue
		}
		if strings.Contains(l, "| while read") {
			t.Errorf("a `while read` is fed by a pipe, so it runs in a subshell: %s", l)
		}
	}
}
