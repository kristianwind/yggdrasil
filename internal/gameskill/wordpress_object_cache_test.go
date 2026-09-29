package gameskill

import (
	"os"
	"strings"
	"testing"
)

func wordpressRune(t *testing.T) *Gameskill {
	t.Helper()
	b, err := os.ReadFile("../../community-runes/apps/wordpress.yaml")
	if err != nil {
		t.Fatalf("read rune: %v", err)
	}
	gs, err := Parse(b)
	if err != nil {
		t.Fatalf("parse rune: %v", err)
	}
	return gs
}

func findService(gs *Gameskill, name string) *Service {
	for i := range gs.Services {
		if gs.Services[i].Name == name {
			return &gs.Services[i]
		}
	}
	return nil
}

// The redis sidecar must be OFF for a server that predates the variable, and off
// by default for a new one. Every live WordPress site on the panel is in the
// first state the moment this rune version is taken, and the failure mode being
// guarded is not subtle: a cache container appearing on three production sites
// nobody asked.
func TestWordPressObjectCacheIsOptIn(t *testing.T) {
	gs := wordpressRune(t)
	redis := findService(gs, "redis")
	if redis == nil {
		t.Fatal("the wordpress rune has no redis sidecar")
	}

	for _, tc := range []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"an existing server, created before the variable existed", map[string]string{"DB_NAME": "wp"}, false},
		{"a new server left at the default", map[string]string{"OBJECT_CACHE": "false"}, false},
		{"the operator turned it on", map[string]string{"OBJECT_CACHE": "true"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := redis.IsEnabled(tc.env)
			if err != nil {
				t.Fatalf("IsEnabled: %v", err)
			}
			if got != tc.want {
				t.Errorf("redis enabled = %v, want %v", got, tc.want)
			}
		})
	}

	var cache *Variable
	for i := range gs.Variables {
		if gs.Variables[i].Key == "OBJECT_CACHE" {
			cache = &gs.Variables[i]
		}
	}
	if cache == nil {
		t.Fatal("no OBJECT_CACHE variable")
	}
	if cache.Type != "bool" {
		t.Errorf("OBJECT_CACHE type = %q, want bool", cache.Type)
	}
	if cache.Default != false {
		t.Errorf("OBJECT_CACHE default = %v, want false — it must not switch itself on", cache.Default)
	}
}

// A cache must not keep a volume: a restart is how you clear it, and a persisted
// one would carry the stale entries straight back across.
func TestWordPressRedisPersistsNothing(t *testing.T) {
	redis := findService(wordpressRune(t), "redis")
	if redis.DataPath != "" {
		t.Errorf("redis declares data_path %q — a cache has nothing worth persisting", redis.DataPath)
	}
	cmd := strings.Join(redis.Command, " ")
	if !strings.Contains(cmd, "--maxmemory") {
		t.Errorf("redis runs without a memory limit: %q — an unbounded cache on a shared box "+
			"grows until something else is killed", cmd)
	}
	// redis defaults to noeviction, which answers a full cache with write ERRORS
	// rather than evicting. Inside WordPress that surfaces on the request, not in
	// a log nobody reads.
	if !strings.Contains(cmd, "allkeys-lru") {
		t.Errorf("redis does not set allkeys-lru: %q — a full cache would start refusing writes", cmd)
	}
	if len(redis.Ports) != 0 {
		t.Errorf("redis publishes %d host port(s) — an unauthenticated cache must stay on the "+
			"per-server network", len(redis.Ports))
	}
}
