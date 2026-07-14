package api

import (
	"strings"
	"testing"
)

func TestParseAIPlan(t *testing.T) {
	// Plain JSON.
	acts, note := parseAIPlan(`{"actions":[{"action":"safe_restart","server":"Asgard","reason":"nightly"}],"note":"ok"}`)
	if len(acts) != 1 || acts[0].Action != "safe_restart" || acts[0].Server != "Asgard" || note != "ok" {
		t.Fatalf("plain: %+v note=%q", acts, note)
	}
	// Wrapped in a markdown code fence.
	acts, _ = parseAIPlan("```json\n{\"actions\":[{\"action\":\"stop\",\"server\":\"Bifrost\"}]}\n```")
	if len(acts) != 1 || acts[0].Action != "stop" {
		t.Fatalf("fenced: %+v", acts)
	}
	// Prose around the object.
	acts, _ = parseAIPlan(`Sure! {"actions":[{"action":"start","server":"X"}]} hope that helps`)
	if len(acts) != 1 || acts[0].Action != "start" {
		t.Fatalf("prose: %+v", acts)
	}
	// Garbage → empty + a note.
	acts, note = parseAIPlan("I cannot do that")
	if len(acts) != 0 || note == "" {
		t.Fatalf("garbage: %+v note=%q", acts, note)
	}
}

func TestAIAllowedActionsExcludesDestructive(t *testing.T) {
	for _, bad := range []string{"wipe", "delete", "command", "update", "backup"} {
		if aiAllowedActions[bad] {
			t.Errorf("%q must NOT be an AI-proposable action", bad)
		}
	}
	for _, ok := range []string{"restart", "safe_restart", "stop", "start"} {
		if !aiAllowedActions[ok] {
			t.Errorf("%q should be allowed", ok)
		}
	}
}

func TestBuildAIPlanMessages(t *testing.T) {
	msgs := buildAIPlanMessages("restart the minecraft servers", []serverRow{
		{ID: "1", Name: "Asgard", GameskillID: "minecraft-java", Status: "running"},
	})
	sys := strings.ToLower(msgs[0].Content)
	for _, want := range []string{"json", "cannot wipe", "untrusted", "safe_restart"} {
		if !strings.Contains(sys, want) {
			t.Errorf("plan prompt missing %q", want)
		}
	}
	if !strings.Contains(msgs[1].Content, "Asgard") || !strings.Contains(msgs[1].Content, "restart the minecraft") {
		t.Errorf("user prompt missing server or request:\n%s", msgs[1].Content)
	}
}
