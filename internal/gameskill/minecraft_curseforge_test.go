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
