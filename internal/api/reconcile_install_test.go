package api

import (
	"testing"
)

// An install/update removes the container and puts a new one back. The status
// reconciler runs every 20 seconds, so it caught that gap and filed a crash for
// work the operator had asked for.
//
// Measured on a fleet with a nightly "update all" schedule: 71 crash rows in 45
// days, 51 of them inside the two hours that schedule runs, and eleven servers
// "crashing" within four seconds of each other — all exit 0. The cost is not the
// rows themselves; it is that a real crash is invisible among them, in the very
// history built to surface it.
func TestNoCrashRecordedWhileAnInstallIsRunning(t *testing.T) {
	s := testServer(t)
	s.install = newProgressHub()
	id := seedServer(t, s, "updating.dk")

	// Same shape both times: the container is gone and the panel finds out.
	// The only difference is whether the panel is the one that took it away.
	s.install.setActive(id, true)
	if got := s.shouldRecordCrash(id); got {
		t.Error("an install is running — the container vanishing is the install doing its job, not a crash")
	}

	s.install.setActive(id, false)
	if got := s.shouldRecordCrash(id); !got {
		t.Error("no install running — a container that disappeared is exactly what the crash history is for")
	}
}

// A server that has never been installed has no entry in the hub at all, which
// must read as "not installing" rather than as missing state.
func TestUnknownServerCountsAsNotInstalling(t *testing.T) {
	s := testServer(t)
	s.install = newProgressHub()
	if !s.shouldRecordCrash("never-seen") {
		t.Error("an unknown server is not mid-install; its crashes must still be recorded")
	}
}
