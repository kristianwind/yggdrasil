package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeCollector stands in for a receiving panel and records what it was told.
func fakeCollector(t *testing.T) (url string, got *[]beaconPayload) {
	t.Helper()
	var mu sync.Mutex
	seen := []beaconPayload{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p beaconPayload
		json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		seen = append(seen, p)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &seen
}

// A panel that updates must report its new version promptly.
//
// The gate used to be the day alone, so a panel that auto-updated at 03:00 had
// already pinged that day and kept reporting its pre-update version for up to 24
// hours — stale exactly when you'd want to see who's on the new release. Both
// live panels were caught doing it: running v0.2.160, reported as v0.2.158/159.
func TestBeaconRePingsWhenTheVersionChanges(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	url, seen := fakeCollector(t)
	s.setSetting(ctx, "beacon_enabled", "1")
	s.setSetting(ctx, "beacon_url", url)

	s.version = "v0.2.159"
	s.maybeSendBeacon()
	if len(*seen) != 1 {
		t.Fatalf("first ping: got %d, want 1", len(*seen))
	}

	// Same version, same day: stay quiet. The daily budget is the point.
	s.maybeSendBeacon()
	s.maybeSendBeacon()
	if len(*seen) != 1 {
		t.Errorf("pinged %d times on one version in one day, want 1", len(*seen))
	}

	// The panel updates and restarts. The day hasn't changed — this is the case
	// that was silently wrong.
	s.version = "v0.2.160"
	s.maybeSendBeacon()
	if len(*seen) != 2 {
		t.Fatalf("an updated panel didn't re-report: got %d pings, want 2", len(*seen))
	}
	if v := (*seen)[1].Version; v != "v0.2.160" {
		t.Errorf("re-reported as %q, want the new version", v)
	}

	// And it settles again on the new version.
	s.maybeSendBeacon()
	if len(*seen) != 2 {
		t.Errorf("kept pinging after reporting the new version: %d", len(*seen))
	}
}

// A panel upgrading to this fix has beacon_last_day set but no recorded version,
// so it re-reports once and is correct from then on — no migration needed.
func TestBeaconReportsAfterUpgradeFromDayOnlyGate(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	url, seen := fakeCollector(t)
	s.setSetting(ctx, "beacon_enabled", "1")
	s.setSetting(ctx, "beacon_url", url)
	s.version = "v0.2.161"

	// The state an existing panel wakes up with: pinged today, version unrecorded.
	s.setSetting(ctx, "beacon_last_day", time.Now().UTC().Format("2006-01-02"))

	s.maybeSendBeacon()
	if len(*seen) != 1 {
		t.Fatalf("an upgraded panel should report its version once: got %d", len(*seen))
	}
	if v := (*seen)[0].Version; v != "v0.2.161" {
		t.Errorf("reported %q", v)
	}
}

// Opting out still means silence — and it has to keep meaning that. The beacon
// is on by default now, so "off" is the only state a user can be in that an
// upgrade could quietly undo, and undoing it would be the exact betrayal the
// disclosure is meant to prevent.
func TestBeaconStaysOffWhenExplicitlyDisabled(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	url, seen := fakeCollector(t)
	s.setSetting(ctx, "beacon_url", url)
	s.setSetting(ctx, "beacon_enabled", "0")
	s.version = "v0.2.160"
	s.maybeSendBeacon()
	if len(*seen) != 0 {
		t.Errorf("an explicitly disabled beacon sent %d pings", len(*seen))
	}
	if s.beaconEnabled(ctx) {
		t.Error("an explicit off must not read as enabled")
	}
}

// An install that has never expressed a preference gets the default, which is on.
func TestBeaconOnByDefaultWhenUnset(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	url, seen := fakeCollector(t)
	s.setSetting(ctx, "beacon_url", url)
	s.version = "v0.2.160"

	if !s.beaconEnabled(ctx) {
		t.Fatal("an untouched install should get the default, which is on")
	}
	s.maybeSendBeacon()
	if len(*seen) != 1 {
		t.Fatalf("expected one ping, got %d", len(*seen))
	}
}

// The notice is the condition the default rests on: it is pending exactly while
// the operator has not chosen, and never again once they have.
func TestBeaconNoticePendingOnlyBeforeAChoice(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()

	if !s.beaconNoticePending(ctx) {
		t.Error("a fresh install has not been told yet — the notice should be pending")
	}
	// Acknowledging settles it whichever way the operator went.
	s.setSetting(ctx, "beacon_notice_ack", "1")
	if s.beaconNoticePending(ctx) {
		t.Error("an acknowledged notice must not come back")
	}

	// And someone who had already made a deliberate choice is not told about a
	// decision they have made — including an operator who turned it ON before
	// this default existed.
	s2 := testServer(t)
	s2.setSetting(ctx, "beacon_enabled", "1")
	if s2.beaconNoticePending(ctx) {
		t.Error("an operator who already opted in has nothing to be told")
	}
	s3 := testServer(t)
	s3.setSetting(ctx, "beacon_enabled", "0")
	if s3.beaconNoticePending(ctx) {
		t.Error("an operator who already opted out has nothing to be told")
	}
}
