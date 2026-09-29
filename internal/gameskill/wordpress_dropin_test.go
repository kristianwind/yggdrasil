package gameskill

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// objectCacheBlock pulls the wp-config wiring out of the rune that actually
// ships, so this test cannot drift from it by being a copy.
func objectCacheBlock(t *testing.T) string {
	t.Helper()
	gs := wordpressRune(t)
	src := strings.Join(gs.Startup.Exec, "\n")
	if src == "" {
		src = gs.Startup.Command
	}
	i := strings.Index(src, "# The object cache wiring")
	j := strings.Index(src, "exec apache2-foreground")
	if i < 0 || j < 0 || j < i {
		t.Fatalf("could not find the object cache block in the rune's startup script")
	}
	return src[i:j]
}

// Run the block the way the container does -- /bin/sh, not bash -- against a real
// wp-config.php, and assert what it DID.
//
// This replaced a test that grepped the rune for the strings it expected to find.
// That version passed with the entire drop-in cleanup deleted from the rune,
// because the surrounding comments still mentioned the words: it answered "does
// this file talk about the drop-in" when the question was "does it remove it".
func runBlock(t *testing.T, dir, objectCache string) string {
	t.Helper()
	script := "set -e\nc=" + filepath.Join(dir, "wp-config.php") + "\n" +
		"export WORDPRESS_DB_NAME=garageristeriet\n" + objectCacheBlock(t)
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(),
		"YGG_OBJECT_CACHE="+objectCache,
		"WORDPRESS_DB_NAME=garageristeriet")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running the rune's block failed: %v\n%s", err, out)
	}
	return string(out)
}

func wpTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "wp-content"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wp-config.php"),
		[]byte("<?php\ndefine( 'DB_NAME', 'x' );\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func config(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "wp-config.php"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestObjectCacheDefinesAreWrittenAndIdempotent(t *testing.T) {
	dir := wpTree(t)
	runBlock(t, dir, "true")
	c := config(t, dir)
	for _, want := range []string{"WP_REDIS_HOST", "'redis'", "WP_CACHE_KEY_SALT", "garageristeriet"} {
		if !strings.Contains(c, want) {
			t.Errorf("wp-config.php has no %s after enabling the cache:\n%s", want, c)
		}
	}
	if n := strings.Count(c, "ygg-object-cache"); n != 1 {
		t.Fatalf("%d marker lines after one run, want 1", n)
	}
	// Every start re-runs this. A block that appends would grow wp-config.php by a
	// line per restart until PHP is parsing a file of duplicate defines.
	runBlock(t, dir, "true")
	if n := strings.Count(config(t, dir), "ygg-object-cache"); n != 1 {
		t.Errorf("%d marker lines after a second run, want 1 — the rewrite is not idempotent", n)
	}
}

func TestTurningTheCacheOffRemovesTheDefines(t *testing.T) {
	dir := wpTree(t)
	runBlock(t, dir, "true")
	runBlock(t, dir, "false")
	if c := config(t, dir); strings.Contains(c, "WP_REDIS_HOST") {
		t.Errorf("the redis defines survived being switched off:\n%s", c)
	}
}

// The one that matters. object-cache.php is what actually routes WordPress at
// redis; the defines alone do nothing. Left behind after the sidecar is gone,
// every request reaches for a container that no longer exists.
func TestTurningTheCacheOffRemovesOurDropInOnly(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		wantGone     bool
	}{
		{"our own redis drop-in", "Plugin Name: Redis Object Cache Drop-In", true},
		{"somebody else's Memcached drop-in", "Plugin Name: Memcached Object Cache", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := wpTree(t)
			dropIn := filepath.Join(dir, "wp-content", "object-cache.php")
			if err := os.WriteFile(dropIn, []byte("<?php\n/*\n"+tc.header+"\n*/\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runBlock(t, dir, "false")
			_, err := os.Stat(dropIn)
			gone := os.IsNotExist(err)
			if gone != tc.wantGone {
				if tc.wantGone {
					t.Errorf("the redis drop-in survived — the site keeps reaching for a cache that is gone")
				} else {
					t.Errorf("a Memcached drop-in was deleted — the rune took out somebody else's cache")
				}
			}
		})
	}
}
