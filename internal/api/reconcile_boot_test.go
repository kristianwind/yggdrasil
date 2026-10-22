package api

import (
	"testing"
	"time"
)

// While autostart is bringing the fleet back, a container at exit 0 is one the
// panel is about to start, not one that stopped — so the reconciler must not
// judge it, or it tears down that server's tunnel routes and DNS (.164,
// 2026-10-22). But a hung autostart must not switch crash detection off for
// good, and a panel that is not booting must behave exactly as before.
func TestReconcileWaitsForAutostart(t *testing.T) {
	now := time.Now()
	s := &Server{}
	if s.reconcileHeld(now) {
		t.Error("a panel that is not booting must reconcile as before")
	}

	s.booting.Store(true)
	s.bootDeadline = now.Add(autostartGrace)
	if !s.reconcileHeld(now) {
		t.Error("while autostart is running, the reconciler must wait")
	}
	if s.reconcileHeld(now.Add(autostartGrace + time.Second)) {
		t.Error("past the grace period the reconciler must run even if autostart never finished")
	}

	s.booting.Store(false) // autostart finished
	if s.reconcileHeld(now) {
		t.Error("once autostart has finished, the reconciler must run")
	}
}
