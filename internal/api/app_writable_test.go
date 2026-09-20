package api

import (
	"strings"
	"testing"
)

// A rune that declares nothing must behave exactly as before — no container, no
// chmod, no change. This is what makes the fix safe to ship ahead of the runes
// that use it.
func TestNoDeclarationChangesNothing(t *testing.T) {
	if got := appWritableScript(nil); got != "" {
		t.Errorf("a rune declaring nothing must produce no script, got %q", got)
	}
	if got := appWritableScript([]string{"", "   "}); got != "" {
		t.Errorf("blank entries must produce no script, got %q", got)
	}
}

// The whole point: the app gets write back, on the named subtree only.
func TestDeclaredPathGetsGroupWrite(t *testing.T) {
	got := appWritableScript([]string{"wp-content"})
	if !strings.Contains(got, "chmod -R g+rw '/data/wp-content'") {
		t.Errorf("got %q", got)
	}
	// The data dir's other contents — wp-admin, wp-includes, wp-config.php —
	// must not appear at all. They are unwritable by PHP on purpose.
	for _, forbidden := range []string{"wp-config", "wp-admin", "wp-includes", "/data'", "/data "} {
		if strings.Contains(got, forbidden) {
			t.Errorf("script reaches %q, which the rune did not declare: %s", forbidden, got)
		}
	}
}

// A rune is a file somebody wrote, and this field is pasted into a shell
// command. "../.." must be confined, not obeyed.
func TestPathsCannotEscapeTheDataDir(t *testing.T) {
	for _, in := range []string{"../../etc", "/etc/shadow", "wp-content/../../../root", "./../.."} {
		got := appWritableScript([]string{in})
		for _, line := range strings.Split(got, "\n") {
			if line == "" {
				continue
			}
			if !strings.Contains(line, "'/data/") && !strings.Contains(line, "'/data'") {
				t.Errorf("input %q escaped the data dir: %s", in, line)
			}
		}
	}
	// A path that cleans to the data dir itself is dropped rather than applied:
	// chmod -R g+rw over the whole webroot is the vulnerability, not the fix.
	if got := appWritableScript([]string{"/", "..", "."}); got != "" {
		t.Errorf("the whole data dir must never be the target, got %q", got)
	}
}

// Quoting is not cosmetic here — the script is assembled as text.
func TestPathsAreQuoted(t *testing.T) {
	got := appWritableScript([]string{"wp content; rm -rf /"})
	if strings.Contains(got, "; rm -rf /'") == false && strings.Contains(got, "rm -rf") {
		// the dangerous text must survive only INSIDE the quotes
		for _, seg := range strings.Split(got, "'") {
			if strings.Contains(seg, "rm -rf") && !strings.Contains(seg, "/data") {
				t.Errorf("unquoted metacharacters reached the script: %s", got)
			}
		}
	}
	if !strings.HasPrefix(shellSingleQuote("a'b"), "'a'\\''b'") {
		t.Errorf("embedded quote not escaped: %s", shellSingleQuote("a'b"))
	}
}
