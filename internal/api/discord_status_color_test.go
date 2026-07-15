package api

import (
	"encoding/json"
	"fmt"
	"testing"
)

func statusWith(total, online int) publicStatusResponse {
	st := publicStatusResponse{Title: "Test"}
	for i := 0; i < total; i++ {
		s := publicServerStatus{Name: fmt.Sprintf("srv-%d", i), Game: "minecraft", Status: "offline"}
		if i < online {
			s.Status = "online"
		}
		st.Servers = append(st.Servers, s)
	}
	return st
}

func embedOf(t *testing.T, st publicStatusResponse) map[string]any {
	t.Helper()
	var payload struct {
		Embeds []map[string]any `json:"embeds"`
	}
	if err := json.Unmarshal(discordStatusPayload(st), &payload); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if len(payload.Embeds) != 1 {
		t.Fatalf("expected 1 embed, got %d", len(payload.Embeds))
	}
	return payload.Embeds[0]
}

// The colour compares `online` against every server, so counting it inside the
// render loop — which stops at Discord's 25-field cap — made green unreachable
// for a fleet bigger than the cap: 30 healthy servers counted 25, and the board
// sat on amber forever.
func TestDiscordStatusColourCountsEveryServer(t *testing.T) {
	cases := []struct {
		total, online int
		want          int
		why           string
	}{
		{3, 3, discordGreen, "small fleet, all up"},
		{3, 1, discordAmber, "small fleet, partly up"},
		{3, 0, discordGrey, "small fleet, all down"},
		{30, 30, discordGreen, "fleet larger than the field cap, all up"},
		{40, 40, discordGreen, "well past the cap, all up"},
		{30, 26, discordAmber, "past the cap, partly up"},
		{30, 0, discordGrey, "past the cap, all down"},
	}
	for _, c := range cases {
		got := embedOf(t, statusWith(c.total, c.online))["color"]
		if int(got.(float64)) != c.want {
			t.Errorf("%s (%d/%d online): colour = %v, want %v", c.why, c.online, c.total, got, c.want)
		}
	}
}

// Servers beyond the cap must not vanish without a word — the description counts
// all of them, so a silently short list reads as "that's everything".
func TestDiscordStatusDisclosesTruncation(t *testing.T) {
	e := embedOf(t, statusWith(30, 30))
	if n := len(e["fields"].([]any)); n != 25 {
		t.Errorf("rendered %d fields, want Discord's cap of 25", n)
	}
	footer := e["footer"].(map[string]any)["text"].(string)
	if footer == "Yggdrasil Panel · auto-updated" {
		t.Error("30 servers rendered as 25 with no mention that 5 are hidden")
	}
	if desc := e["description"].(string); desc != "**30 of 30 online**" {
		t.Errorf("description = %q, want it to count every server", desc)
	}

	// A fleet that fits must not carry a truncation note.
	e = embedOf(t, statusWith(5, 5))
	if footer := e["footer"].(map[string]any)["text"].(string); footer != "Yggdrasil Panel · auto-updated" {
		t.Errorf("5 servers fit, but the footer claims truncation: %q", footer)
	}
}
