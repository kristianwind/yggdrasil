package gameskill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// All rune YAML files shipped in the repo (builtin + community) must Parse and
// validate — this catches a malformed regex, bad wipe path, or invalid restart
// warning in a shipped rune before it reaches a user's import.
func TestShippedRunesParse(t *testing.T) {
	for _, dir := range []string{"../../builtin-runes", "../../community-runes"} {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Errorf("%s: read: %v", path, rerr)
				return nil
			}
			if _, perr := Parse(data); perr != nil {
				t.Errorf("%s: %v", path, perr)
			}
			return nil
		})
	}
}

// The panel injects <NAME>_PORT, the allocated HOST port, for every declared
// port, and that value wins over a rune variable of the same name. A rune that
// declares such a variable has a setting that never takes effect, and if it
// binds to it inside the container it listens on the host port number, where
// nothing is forwarded (the container side is the port's default). That is how
// every Minecraft Java server lost RCON, and how Genshin bound its game port to
// a number nothing reached. Steam runes are the exception: they publish 1:1, so
// the host port IS the container port and reading <NAME>_PORT is the intent.
func TestShippedRunesDoNotShadowInjectedPorts(t *testing.T) {
	checked := 0
	for _, dir := range []string{"../../builtin-runes", "../../community-runes"} {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			gs, perr := Parse(data)
			if perr != nil || gs.Steam != nil {
				return nil
			}
			checked++
			injected := map[string]bool{}
			for _, p := range gs.Ports {
				injected[strings.ToUpper(p.Name)+"_PORT"] = true
			}
			for _, v := range gs.Variables {
				if injected[v.Key] {
					t.Errorf("%s: variable %s is always overwritten by the panel's injected host port — bind to the port's default inside the container, and use {{%s}} only to advertise", path, v.Key, v.Key)
				}
			}
			return nil
		})
	}
	if checked < 20 {
		t.Fatalf("checked only %d runes — the walk is not reading the catalogue", checked)
	}
	t.Logf("checked %d non-Steam runes", checked)
}
