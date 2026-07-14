package api

import (
	"strings"
	"testing"
)

func TestBuildDigestMessages(t *testing.T) {
	events := []adminLogEvent{
		{Time: "16:10:31", Type: "death", Player: "Charlie"},
		{Time: "16:02:55", Type: "kill", Player: "Alice"},
		{Time: "15:04:23", Type: "join", Player: "Bob"},
	}
	msgs := buildDigestMessages("Chernarus", events)
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("expected system+user messages, got %+v", msgs)
	}
	// The user message must carry the server name and every event's player + type.
	u := msgs[1].Content
	for _, want := range []string{"Chernarus", "Charlie", "death", "Alice", "kill", "Bob", "join"} {
		if !strings.Contains(u, want) {
			t.Errorf("user prompt missing %q:\n%s", want, u)
		}
	}
	// The system prompt must forbid inventing events + automated action (safety).
	sys := strings.ToLower(msgs[0].Content)
	if !strings.Contains(sys, "advisory") || !strings.Contains(sys, "do not invent") {
		t.Errorf("system prompt missing advisory/no-invent guardrails:\n%s", msgs[0].Content)
	}
	// Prompt-injection guardrail: player names are untrusted and must be treated as data.
	if !strings.Contains(sys, "untrusted") {
		t.Errorf("system prompt missing prompt-injection guardrail:\n%s", msgs[0].Content)
	}
}

func TestBuildExplainMessages(t *testing.T) {
	msgs := buildExplainMessages("dayz", "install", "ERROR: not enough disk quota")
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("expected system+user, got %+v", msgs)
	}
	sys := strings.ToLower(msgs[0].Content)
	// Must ask for cause + fix, and carry the injection guardrail + context hint.
	for _, want := range []string{"cause", "fix", "untrusted", "install"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q:\n%s", want, msgs[0].Content)
		}
	}
	if !strings.Contains(msgs[1].Content, "dayz") || !strings.Contains(msgs[1].Content, "not enough disk quota") {
		t.Errorf("user prompt missing rune id or log:\n%s", msgs[1].Content)
	}
}

func TestBuildDigestMessagesCaps(t *testing.T) {
	var events []adminLogEvent
	for i := 0; i < 500; i++ {
		events = append(events, adminLogEvent{Time: "00:00:00", Type: "join", Player: "P"})
	}
	u := buildDigestMessages("S", events)[1].Content
	// Capped at 200 event lines (+ 2 header lines).
	if n := strings.Count(u, "\n"); n > 205 {
		t.Errorf("expected the event list capped near 200 lines, got %d", n)
	}
}

func TestBuildHealthDigestMessages(t *testing.T) {
	msgs := buildHealthDigestMessages("Servers: 5 total, 4 running, 1 not running.\nNot running: Midgard (stopped)")
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("expected system+user, got %+v", msgs)
	}
	sys := strings.ToLower(msgs[0].Content)
	for _, want := range []string{"attention", "advisory", "do not invent"} {
		if !strings.Contains(sys, want) {
			t.Errorf("health system prompt missing %q:\n%s", want, msgs[0].Content)
		}
	}
	if !strings.Contains(msgs[1].Content, "Midgard") {
		t.Errorf("snapshot not carried into user prompt:\n%s", msgs[1].Content)
	}
}
