package api

import (
	"context"
	"strings"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

func TestSyncRuneWatchers(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	gs := &gameskill.Gameskill{Watchers: []gameskill.Watcher{
		{Name: "PHP fatal errors", Pattern: "PHP Fatal error", Action: "kvasir"},
		{Name: "Failed logins", Pattern: "(?i)authentication failure", Threshold: 5, WindowSecs: 120},
	}}

	s.syncRuneWatchers(ctx, "srv-1", gs)

	count := func() int {
		var n int
		s.db.QueryRow("SELECT COUNT(*) FROM log_watchers WHERE server_id='srv-1' AND source='rune'").Scan(&n)
		return n
	}
	if count() != 2 {
		t.Fatalf("expected 2 seeded watchers, got %d", count())
	}
	// Defaults applied where the rune omitted them.
	var thr, win int
	var action string
	s.db.QueryRow("SELECT threshold, window_secs, action FROM log_watchers WHERE server_id='srv-1' AND name='PHP fatal errors'").
		Scan(&thr, &win, &action)
	if thr != 1 || win != 60 || action != "kvasir" {
		t.Fatalf("defaults not applied: thr=%d win=%d action=%q", thr, win, action)
	}

	// Idempotent: a second sync (e.g. reinstall) adds nothing.
	s.syncRuneWatchers(ctx, "srv-1", gs)
	if count() != 2 {
		t.Fatalf("resync duplicated watchers: got %d", count())
	}

	// A user edit survives a resync — disabled stays disabled.
	s.db.Exec("UPDATE log_watchers SET enabled=0 WHERE server_id='srv-1' AND name='Failed logins'")
	s.syncRuneWatchers(ctx, "srv-1", gs)
	var enabled int
	s.db.QueryRow("SELECT enabled FROM log_watchers WHERE server_id='srv-1' AND name='Failed logins'").Scan(&enabled)
	if enabled != 0 {
		t.Fatal("resync re-enabled a watcher the user disabled")
	}

	// A rune update that adds a watcher tops up only the missing one.
	gs.Watchers = append(gs.Watchers, gameskill.Watcher{Name: "OOM", Pattern: "Out of memory"})
	s.syncRuneWatchers(ctx, "srv-1", gs)
	if count() != 3 {
		t.Fatalf("expected top-up to 3 watchers, got %d", count())
	}
}

func TestParseWatcherSuggestions(t *testing.T) {
	// Tolerant extraction: prose + fences around the array; invalid regex and
	// duplicates dropped; bounds clamped; action normalized.
	out := "Here you go:\n```json\n[" +
		`{"name":"HTTP 5xx spike","pattern":"\" 5\\d\\d ","threshold":0,"window_secs":9999,"action":"weird","reason":"errors"},` +
		`{"name":"Bad regex","pattern":"([","threshold":1,"window_secs":60,"action":"notify","reason":"broken"},` +
		`{"name":"Existing rule","pattern":"x","threshold":1,"window_secs":60,"action":"notify","reason":"dupe"},` +
		`{"name":"HTTP 5xx spike","pattern":"dup name","threshold":1,"window_secs":60,"action":"notify","reason":"dupe2"}` +
		"]\n```"
	got := parseWatcherSuggestions(out, []string{"Existing rule /x/"})
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 surviving suggestion, got %d: %+v", len(got), got)
	}
	sg := got[0]
	if sg.Name != "HTTP 5xx spike" || sg.Threshold != 1 || sg.WindowSecs != watcherMaxWindow || sg.Action != "notify" {
		t.Fatalf("clamps/normalization wrong: %+v", sg)
	}

	if got := parseWatcherSuggestions("no json here", nil); len(got) != 0 {
		t.Fatalf("garbage input should yield no suggestions, got %+v", got)
	}
}

func TestRuneWatcherValidation(t *testing.T) {
	base := `
gameskill:
  id: test-app
  name: Test App
  category: app
  version: 1
  docker:
    image: nginx:alpine
  startup:
    command: "nginx"
  ports:
    - name: web
      default: 8080
      protocol: tcp
`
	ok := base + `
  watchers:
    - name: Fatal errors
      pattern: "PHP Fatal error"
      action: kvasir
`
	if _, err := gameskill.Parse([]byte(ok)); err != nil {
		t.Fatalf("valid watchers rejected: %v", err)
	}
	bad := base + `
  watchers:
    - name: Broken
      pattern: "(["
`
	if _, err := gameskill.Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), "does not compile") {
		t.Fatalf("bad watcher regex accepted (err=%v)", err)
	}
	badAction := base + `
  watchers:
    - name: Odd
      pattern: "x"
      action: reboot
`
	if _, err := gameskill.Parse([]byte(badAction)); err == nil || !strings.Contains(err.Error(), "notify or kvasir") {
		t.Fatalf("bad watcher action accepted (err=%v)", err)
	}
}
