package api

import "testing"

func TestAlarmStepEdges(t *testing.T) {
	over := map[string]int{}
	firing := map[string]bool{}
	const id = "s1"

	// Below threshold: never fires.
	for i := 0; i < 3; i++ {
		if f, c := alarmStep(over, firing, id, 40, 80); f || c {
			t.Fatalf("below-threshold sample fired/cleared: f=%v c=%v", f, c)
		}
	}
	// At/above threshold: fires only on the alarmBreaches'th consecutive sample.
	for i := 1; i < alarmBreaches; i++ {
		if f, _ := alarmStep(over, firing, id, 90, 80); f {
			t.Fatalf("fired too early at breach %d", i)
		}
	}
	if f, c := alarmStep(over, firing, id, 90, 80); !f || c {
		t.Fatalf("expected fire on breach %d: f=%v c=%v", alarmBreaches, f, c)
	}
	// Still over: does NOT re-fire (edge-triggered).
	if f, _ := alarmStep(over, firing, id, 95, 80); f {
		t.Fatal("re-fired while already alarming")
	}
	// Drops below: clears exactly once.
	if f, c := alarmStep(over, firing, id, 10, 80); f || !c {
		t.Fatalf("expected clear on recovery: f=%v c=%v", f, c)
	}
	if _, c := alarmStep(over, firing, id, 10, 80); c {
		t.Fatal("cleared twice")
	}
}

// A disabled threshold (0) never fires and resets any accumulated state.
func TestAlarmStepDisabled(t *testing.T) {
	over := map[string]int{"s1": 5}
	firing := map[string]bool{"s1": true}
	f, c := alarmStep(over, firing, "s1", 100, 0)
	if f || c {
		t.Fatalf("disabled threshold fired/cleared: f=%v c=%v", f, c)
	}
	if over["s1"] != 0 || firing["s1"] {
		t.Fatalf("disabled threshold left state: over=%d firing=%v", over["s1"], firing["s1"])
	}
}
