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
