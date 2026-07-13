package api

import "testing"

// The DayZ BattlEye `players` regex must parse real rows and skip the header,
// separator, and total lines around them.
func TestParsePlayersDayZ(t *testing.T) {
	const pattern = `^(?P<id>\d+)\s+(?P<ip>[0-9.]+):\d+\s+(?P<ping>\d+)\s+(?P<guid>[0-9a-f]+)\s*\((?:OK|\?)\)\s+(?P<name>.+?)\s*$`
	const output = `Players on server:
[#] [IP Address]:[Port] [Ping] [GUID] [Name]
--------------------------------------------------
0   45.83.19.12:2304      31   9a8b7c6d5e4f(OK) SurvivorBob
1   82.14.5.6:63212       102  1122334455aa(?) Alice Cooper
(2 players in total)`

	players, err := parsePlayers(output, pattern)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(players) != 2 {
		t.Fatalf("got %d players, want 2: %+v", len(players), players)
	}
	if players[0].ID != "0" || players[0].Name != "SurvivorBob" || players[0].Ping != "31" {
		t.Errorf("player 0 wrong: %+v", players[0])
	}
	if players[1].Name != "Alice Cooper" || players[1].IP != "82.14.5.6" {
		t.Errorf("player 1 wrong: %+v", players[1])
	}
}

func TestParsePlayersEmpty(t *testing.T) {
	const pattern = `^(?P<id>\d+)\s+(?P<name>.+)$`
	players, err := parsePlayers("Players on server:\n(0 players in total)", pattern)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(players) != 0 {
		t.Fatalf("expected no players, got %+v", players)
	}
}

// A crafted name/reason must not smuggle a second console line into a kick.
func TestTemplatePlayerCmdSanitizes(t *testing.T) {
	cmd := templatePlayerCmd("kick {{id}} {{reason}}", map[string]string{
		"id":     "3",
		"reason": "spam\n#shutdown",
	})
	if cmd != "kick 3 spam #shutdown" {
		t.Errorf("newline not neutralized: %q", cmd)
	}
}
