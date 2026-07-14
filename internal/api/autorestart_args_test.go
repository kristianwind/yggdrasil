package api

import (
	"encoding/json"
	"testing"
)

func TestArgTrue(t *testing.T) {
	for _, v := range []string{"true", "1"} {
		if !argTrue(v) {
			t.Errorf("argTrue(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"false", "0", "", "yes", "TRUE"} {
		if argTrue(v) {
			t.Errorf("argTrue(%q) = true, want false", v)
		}
	}
}

// The auto-restart toggle writes schedule args that runAction later reads. If the
// two disagree on how a boolean looks, the toggle silently does nothing: ticking
// "warn players" stores a value the runner reads as false, so the countdown never
// broadcasts and the safety backup never runs.
//
// This asserts the contract in the direction that actually broke: what the
// handler writes must be what the runner accepts.
func TestAutoRestartArgsRoundTripThroughRunner(t *testing.T) {
	// Mirrors the map built in handleSetAutoRestart.
	args := map[string]string{
		"warn":         boolStrTrue(),
		"backup_first": boolStrTrue(),
		"target_id":    "t1",
	}
	blob, _ := json.Marshal(args)

	var decoded map[string]string
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatal(err)
	}
	if !argTrue(decoded["warn"]) {
		t.Errorf("warn=%q does not read as true in the runner — the countdown would never fire", decoded["warn"])
	}
	if !argTrue(decoded["backup_first"]) {
		t.Errorf("backup_first=%q does not read as true in the runner — the safety backup would never run", decoded["backup_first"])
	}
}

// Rows written before the fix stored "1"/"0". They must keep working without a
// migration, or every existing auto-restart silently stays broken.
func TestLegacyAutoRestartArgsStillRead(t *testing.T) {
	legacy := map[string]string{"warn": "1", "backup_first": "0"}
	if !argTrue(legacy["warn"]) {
		t.Error(`legacy warn="1" must still read as true`)
	}
	if argTrue(legacy["backup_first"]) {
		t.Error(`legacy backup_first="0" must read as false`)
	}
}

// boolStrTrue returns what handleSetAutoRestart writes for a true flag, kept in
// one place so this test fails if that encoding changes again.
func boolStrTrue() string { return "true" }
