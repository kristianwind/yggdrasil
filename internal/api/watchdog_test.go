package api

import (
	"testing"
	"time"
)

// A run of failures below the threshold must not heal; the threshold'th one does,
// and a success anywhere in between resets the streak.
func TestWatchdogStreak(t *testing.T) {
	w := newWatchdogState()
	now := time.Now()
	const id = "srv1"

	for i := 1; i < watchdogFailThreshold; i++ {
		if heal, n := w.recordResult(id, false, now); heal || n != i {
			t.Fatalf("fail %d: heal=%v n=%d, want heal=false n=%d", i, heal, n, i)
		}
	}
	// A success resets the streak — the next failure starts over at 1.
	if heal, _ := w.recordResult(id, true, now); heal {
		t.Fatal("success should not heal")
	}
	if heal, n := w.recordResult(id, false, now); heal || n != 1 {
		t.Fatalf("after reset: heal=%v n=%d, want heal=false n=1", heal, n)
	}
	// Now drive to the threshold — the last one heals.
	for i := 2; i < watchdogFailThreshold; i++ {
		w.recordResult(id, false, now)
	}
	if heal, n := w.recordResult(id, false, now); !heal || n != watchdogFailThreshold {
		t.Fatalf("threshold: heal=%v n=%d, want heal=true n=%d", heal, n, watchdogFailThreshold)
	}
}

// After a heal the server is in cooldown, and the streak is reset so it starts
// fresh once the cooldown expires.
func TestWatchdogCooldown(t *testing.T) {
	w := newWatchdogState()
	now := time.Now()
	const id = "srv1"

	for i := 0; i < watchdogFailThreshold; i++ {
		w.recordResult(id, false, now)
	}
	if !w.inCooldown(id, now.Add(1*time.Minute)) {
		t.Fatal("expected cooldown right after a heal")
	}
	if w.inCooldown(id, now.Add(watchdogCooldown+time.Second)) {
		t.Fatal("cooldown should have expired")
	}
	// Streak was reset at heal time — a single failure is only 1, not threshold.
	if heal, n := w.recordResult(id, false, now.Add(watchdogCooldown+2*time.Second)); heal || n != 1 {
		t.Fatalf("post-cooldown: heal=%v n=%d, want heal=false n=1", heal, n)
	}
}

func TestWatchdogCrashLoopQuarantine(t *testing.T) {
	w := newWatchdogState()
	now := time.Now()
	const id = "loopy"
	// Heals under the threshold don't quarantine.
	for i := 1; i < quarantineThreshold; i++ {
		if w.recordHeal(id, now.Add(time.Duration(i)*time.Minute)) {
			t.Fatalf("quarantined too early at heal %d", i)
		}
		if w.isQuarantined(id) {
			t.Fatalf("isQuarantined true too early at heal %d", i)
		}
	}
	// The threshold'th heal within the window quarantines (transition returns true once).
	if !w.recordHeal(id, now.Add(time.Duration(quarantineThreshold)*time.Minute)) {
		t.Fatal("expected quarantine on the threshold heal")
	}
	if !w.isQuarantined(id) {
		t.Fatal("expected isQuarantined after threshold")
	}
	// Subsequent heals don't re-trigger the one-time alert.
	if w.recordHeal(id, now.Add(60*time.Minute)) {
		t.Fatal("quarantine transition should only fire once")
	}
}

func TestWatchdogHealsAgeOut(t *testing.T) {
	w := newWatchdogState()
	base := time.Now()
	const id = "slowcrash"
	// Heals spread beyond the window never accumulate to the threshold.
	for i := 0; i < quarantineThreshold+2; i++ {
		if w.recordHeal(id, base.Add(time.Duration(i)*(quarantineWindow+time.Minute))) {
			t.Fatalf("aged-out heals should never quarantine (i=%d)", i)
		}
	}
	if w.isQuarantined(id) {
		t.Fatal("spaced-out heals must not quarantine")
	}
}
