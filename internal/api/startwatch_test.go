package api

import "testing"

// The failure streak counts up per attempt and only tips into "give up" on the
// startMaxAttempts'th failure; a reset (success / manual start) starts it over.
func TestStartStateStreak(t *testing.T) {
	st := newStartState()
	const id = "srv1"

	// The first startMaxAttempts-1 failures still have retry budget left.
	for i := 1; i < startMaxAttempts; i++ {
		if n := st.recordFailure(id); n != i || n >= startMaxAttempts {
			t.Fatalf("failure %d: n=%d, want %d (< %d, keep retrying)", i, n, i, startMaxAttempts)
		}
	}
	// The next one reaches the cap — this is where onStartFailed gives up.
	if n := st.recordFailure(id); n != startMaxAttempts {
		t.Fatalf("final failure: n=%d, want %d (give up)", n, startMaxAttempts)
	}

	// A reset (clean start or operator intent) restores the full budget.
	st.reset(id)
	if n := st.recordFailure(id); n != 1 {
		t.Fatalf("after reset: n=%d, want 1", n)
	}
}

// Streaks are per-server: one server failing must not consume another's budget.
func TestStartStateIsolatedPerServer(t *testing.T) {
	st := newStartState()
	st.recordFailure("a")
	st.recordFailure("a")
	if n := st.recordFailure("b"); n != 1 {
		t.Fatalf("server b: n=%d, want 1 (independent of a)", n)
	}
	if n := st.recordFailure("a"); n != 3 {
		t.Fatalf("server a: n=%d, want 3", n)
	}
}
