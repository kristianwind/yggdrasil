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
