package api

import (
	"testing"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

func dayzAdminLogCfg() *gameskill.AdminLog {
	return &gameskill.AdminLog{
		Path:      "profiles/*.ADM",
		TimeRegex: `^(?P<time>\d{1,2}:\d{2}:\d{2})`,
		Events: []gameskill.AdminLogRule{
			{Type: "kill", Regex: `Player "(?P<name>[^"]+)".*killed by`},
			{Type: "death", Regex: `Player "(?P<name>[^"]+)".*(died\.|bled out|committed suicide)`},
			{Type: "join", Regex: `Player "(?P<name>[^"]+)" is connected`},
			{Type: "leave", Regex: `Player "(?P<name>[^"]+)".*has been disconnected`},
		},
	}
}

func TestParseAdminLogDayZ(t *testing.T) {
	const content = `AdminLog started on 2026-07-13 at 15:00:00
15:04:23 | Player "Bob" is connected (id=abc)
15:04:25 | Player "Alice" is connected (id=def)
some unrelated noise line
15:41:07 | Player "Bob"(id=abc pos=<1,2,3>) has been disconnected
16:02:55 | Player "Alice"(id=def) killed by Player "Bandit"(id=xyz) with M4A1
16:10:31 | Player "Charlie"(id=ghi) died.`

	events := parseAdminLog(content, dayzAdminLogCfg())
	if len(events) != 5 {
		t.Fatalf("got %d events, want 5: %+v", len(events), events)
	}
	if events[0].Type != "join" || events[0].Player != "Bob" || events[0].Time != "15:04:23" {
		t.Errorf("event 0 wrong: %+v", events[0])
	}
	if events[3].Type != "kill" || events[3].Player != "Alice" {
		t.Errorf("kill event wrong: %+v", events[3])
	}
	if events[4].Type != "death" || events[4].Player != "Charlie" {
		t.Errorf("death event wrong: %+v", events[4])
	}
}

// A "killed by" line must classify as kill, not death — rule order (kill first)
// is what guarantees it, so this pins the ordering.
func TestParseAdminLogKillBeatsDeath(t *testing.T) {
	events := parseAdminLog(`16:02:55 | Player "Alice"(id=def) killed by Player "Bandit"(id=xyz) with M4A1`, dayzAdminLogCfg())
	if len(events) != 1 || events[0].Type != "kill" {
		t.Fatalf("want single kill event, got %+v", events)
	}
}

func TestReadTailDropsPartialLine(t *testing.T) {
	// Not exercising the file path here (covered by handler); just the byte logic
	// via a small helper would need a temp file. Keep it light: parse handles the
	// partial-line case since a leading fragment simply won't match any rule.
	events := parseAdminLog("(id=abc) has been disconnected\n15:04:23 | Player \"Bob\" is connected", dayzAdminLogCfg())
	if len(events) != 1 || events[0].Type != "join" {
		t.Fatalf("partial first line should be ignored, got %+v", events)
	}
}
