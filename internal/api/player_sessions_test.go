package api

import (
	"regexp"
	"testing"
)

func TestMatchSessionEvent(t *testing.T) {
	jr := regexp.MustCompile(`Player connected:\s*(.+?),`)
	lr := regexp.MustCompile(`Player disconnected:\s*(.+?),`)

	cases := []struct {
		line, kind, name string
	}{
		// Docker-timestamped Bedrock lines.
		{"2026-07-27T07:44:01.123456789Z [INFO] Player connected: Steve, xuid: 123", "join", "Steve"},
		{"2026-07-27T08:10:00Z [INFO] Player disconnected: Steve, xuid: 123", "leave", "Steve"},
		// A timestamped but unrelated line → no event, but ts is still returned.
		{"2026-07-27T07:45:00Z Server tick took too long", "", ""},
		// A line without a valid leading timestamp is ignored entirely.
		{"[INFO] Player connected: NoTimestamp, xuid: 1", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		kind, name, _ := matchSessionEvent(c.line, jr, lr)
		if kind != c.kind || name != c.name {
			t.Errorf("matchSessionEvent(%q) = (%q,%q); want (%q,%q)", c.line, kind, name, c.kind, c.name)
		}
	}

	// A gamertag with spaces is captured whole (up to the comma).
	if _, name, _ := matchSessionEvent("2026-07-27T07:44:01Z Player connected: Big Bob, xuid: 9", jr, lr); name != "Big Bob" {
		t.Errorf("multi-word gamertag not captured: %q", name)
	}
}
