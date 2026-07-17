package scheduler

import "testing"

func TestRender(t *testing.T) {
	got := Render("say Restarting in {{minutes}} min, {{server_name}}!", map[string]string{
		"minutes":     "5",
		"server_name": "Family SMP",
	})
	want := "say Restarting in 5 min, Family SMP!"
	if got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
}

func TestRenderLeavesUnknownPlaceholders(t *testing.T) {
	got := Render("hello {{unknown}}", map[string]string{"x": "y"})
	if got != "hello {{unknown}}" {
		t.Errorf("unknown placeholder should be preserved, got %q", got)
	}
}

func TestValidAction(t *testing.T) {
	for _, a := range []Action{ActionBackup, ActionRestart, ActionMessage, ActionUpdate, ActionStart, ActionStop, ActionCommand} {
		if !ValidAction(a) {
			t.Errorf("%q should be valid", a)
		}
	}
	if ValidAction("nonsense") {
		t.Error("nonsense should be invalid")
	}
}

func TestValidateCron(t *testing.T) {
	for _, ok := range []string{"0 4 * * *", "*/5 * * * *", "0 0 4 * * *"} {
		if err := ValidateCron(ok); err != nil {
			t.Errorf("%q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "* * *", "a b c d e f g"} {
		if err := ValidateCron(bad); err == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestPlaceholders(t *testing.T) {
	got := Placeholders("say {{server_name}} restarts in {{minutes}}m ({{minutes}} again)")
	want := []string{"server_name", "minutes"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v (order and dedup matter)", got, want)
		}
	}
	if p := Placeholders("say hello"); len(p) != 0 {
		t.Errorf("a body with no placeholders returned %v", p)
	}
}

// Every seeded template must declare only placeholders the scheduler can fill,
// or it ships broken the way "Restart countdown" did.
func TestDefaultTemplatesUseKnownPlaceholders(t *testing.T) {
	known := map[string]bool{"server_name": true, "minutes": true, "seconds": true}
	for _, tmpl := range DefaultTemplates {
		for _, p := range Placeholders(tmpl.Body) {
			if !known[p] {
				t.Errorf("template %q uses {{%s}}, which nothing fills in", tmpl.Name, p)
			}
		}
	}
}
