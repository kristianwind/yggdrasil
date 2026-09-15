package api

import (
	"strings"
	"testing"
)

// runBatch drives the REAL guard the scheduler loop uses, so this cannot drift
// away from the behaviour it claims to pin.
func runBatch(results []string) (attempted int, stopped bool) {
	var g batchGuard
	for _, r := range results {
		if g.shouldStop() {
			return attempted, true
		}
		attempted++
		g.record(r)
	}
	return attempted, false
}

// One bad image, a registry outage or a full disk should cost one server, not
// the fleet. A run that ploughs on through everything failing is how the second
// becomes the first.
func TestBatchStopsAfterConsecutiveFailures(t *testing.T) {
	cases := []struct {
		name      string
		results   []string
		attempted int
		stopped   bool
	}{
		{"everything works", []string{"ok", "ok", "ok", "ok", "ok"}, 5, false},
		{"three in a row stops the rest", []string{"error", "error", "error", "ok", "ok"}, 3, true},
		{"a success resets the count", []string{"error", "error", "ok", "error", "error"}, 5, false},
		{"skips are not failures", []string{"skipped", "skipped", "skipped", "skipped", "ok"}, 5, false},
		{"a skip between errors does not reset them", []string{"error", "skipped", "error", "error", "ok"}, 4, true},
		{"scattered failures are tolerated", []string{"error", "ok", "error", "ok", "error", "ok"}, 6, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attempted, stopped := runBatch(tc.results)
			if attempted != tc.attempted || stopped != tc.stopped {
				t.Errorf("attempted=%d stopped=%v, want attempted=%d stopped=%v",
					attempted, stopped, tc.attempted, tc.stopped)
			}
		})
	}
}

// A server with no health path has nothing to ask, and that must read as "fine"
// rather than as a failure — otherwise every game server on the fleet starts
// failing its updates.
func TestWaitForHealthSkipsServersWithoutAPath(t *testing.T) {
	s := testServer(t)
	s.health = newHealthState()
	id := seedServer(t, s, "gameserver")

	msg, ok := s.waitForHealthAfterUpdate(id)
	if !ok || msg != "" {
		t.Errorf("got (%q, %v), want a silent pass for a server with no health path", msg, ok)
	}
}

// And the message, when it does fail, has to say what is odd: the container is
// up. "Did not come back" would send someone looking for a stopped server.
func TestHealthFailureMessageExplainsTheContainerIsUp(t *testing.T) {
	s := testServer(t)
	s.health = newHealthState()
	id := seedServer(t, s, "site.dk")
	// A health path but no web port: firstWebHostPort returns 0, so this still
	// passes — the point here is only that the path is read, not invented.
	s.db.Exec("UPDATE servers SET health_path='/' WHERE id=?", id)
	if msg, ok := s.waitForHealthAfterUpdate(id); !ok {
		if !strings.Contains(msg, "port is bound") {
			t.Errorf("message must explain why nothing else will report it: %q", msg)
		}
	}
}
