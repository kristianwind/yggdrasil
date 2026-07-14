package api

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestDiskAlarmEdge(t *testing.T) {
	s := testServer(t)
	s.alarms = newAlarmState()
	const id = "d1"

	s.evalDiskAlarm(id, 5000, 4000) // over → fire
	if !s.alarms.diskFiring[id] {
		t.Fatal("expected firing after crossing threshold")
	}
	s.evalDiskAlarm(id, 6000, 4000) // still over → stays firing (no re-eval side effects)
	if !s.alarms.diskFiring[id] {
		t.Fatal("should still be firing")
	}
	s.evalDiskAlarm(id, 1000, 4000) // under → clear
	if s.alarms.diskFiring[id] {
		t.Fatal("expected cleared after dropping below threshold")
	}
	// A disabled (0) threshold never fires.
	s.evalDiskAlarm(id, 9999, 0)
	if s.alarms.diskFiring[id] {
		t.Fatal("disabled threshold must not fire")
	}
}

func TestDirSizeMB(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big"), make([]byte, 3*1024*1024), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "sub", "small"), make([]byte, 512*1024), 0644)
	// 3 MB + 0.5 MB → truncates to 3 whole MB.
	if got := dirSizeMB(dir); got != 3 {
		t.Fatalf("dirSizeMB = %d, want 3", got)
	}
}
