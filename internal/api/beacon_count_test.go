package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedPings inserts distinct installs. instance_id is the primary key, so ids
// have to be unique across calls — reusing them makes the INSERT a silent no-op
// and the count simply doesn't move, which is a confusing way to fail.
var seedN int

func seedPings(t *testing.T, s *Server, recent, stale int) {
	t.Helper()
	ins := func(id, ver, when string) {
		if _, err := s.db.Exec(`INSERT INTO beacon_pings (instance_id, version, first_seen, last_seen, ping_count)
			VALUES (?,?,`+when+`,`+when+`,1)`, id, ver); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	for i := 0; i < recent; i++ {
		seedN++
		ins(fmt.Sprintf("recent-%d", seedN), "v0.2.163", "datetime('now')")
	}
	for i := 0; i < stale; i++ {
		seedN++
		// Pinged once months ago and never again — an install that's gone.
		ins(fmt.Sprintf("stale-%d", seedN), "v0.2.100", "datetime('now','-90 days')")
	}
}

func getCount(t *testing.T, s *Server) (int, publicCount) {
	t.Helper()
	w := httptest.NewRecorder()
	s.handlePublicBeaconCount(w, httptest.NewRequest(http.MethodGet, "/api/beacon/count", nil))
	var pc publicCount
	json.Unmarshal(w.Body.Bytes(), &pc)
	return w.Code, pc
}

// Not a collector, or not opted in: the endpoint isn't there at all. Same as the
// receiver — an endpoint nobody enabled shouldn't advertise itself.
func TestPublicCountIs404UntilOptedIn(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	seedPings(t, s, 100, 0)

	if code, _ := getCount(t, s); code != http.StatusNotFound {
		t.Errorf("with everything off: %d, want 404", code)
	}

	// A collector that hasn't opted into publishing is still 404.
	s.setSetting(ctx, "beacon_receiver_enabled", "1")
	s.invalidatePublicCount()
	if code, _ := getCount(t, s); code != http.StatusNotFound {
		t.Errorf("collector but not publishing: %d, want 404", code)
	}

	// Publishing on, but not a collector — there'd be nothing to count.
	s.setSetting(ctx, "beacon_receiver_enabled", "0")
	s.setSetting(ctx, "beacon_public_count_enabled", "1")
	s.invalidatePublicCount()
	if code, _ := getCount(t, s); code != http.StatusNotFound {
		t.Errorf("publishing but not a collector: %d, want 404", code)
	}
}

// The threshold is the point: below it, the answer is null rather than a small
// number. It's what stops a count that has fallen — a bad release, a collector
// that lost rows, pings that quietly stopped arriving — from being published as
// fact.
func TestPublicCountWithholdsBelowTheThreshold(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	s.setSetting(ctx, "beacon_receiver_enabled", "1")
	s.setSetting(ctx, "beacon_public_count_enabled", "1")
	s.setSetting(ctx, "beacon_public_count_min", "25")

	seedPings(t, s, 24, 0)
	s.invalidatePublicCount()
	code, pc := getCount(t, s)
	if code != http.StatusOK {
		t.Fatalf("below the floor should still answer: %d", code)
	}
	if pc.Count != nil {
		t.Errorf("published %d with a floor of 25 — the floor did nothing", *pc.Count)
	}

	// One more crosses it.
	seedPings(t, s, 1, 0)
	s.invalidatePublicCount()
	_, pc = getCount(t, s)
	if pc.Count == nil {
		t.Fatal("25 installs with a floor of 25 published nothing")
	}
	if *pc.Count != 25 {
		t.Errorf("published %d, want 25", *pc.Count)
	}
	if pc.WindowDays != 30 {
		t.Errorf("window_days = %d, want 30", pc.WindowDays)
	}
}

// It reports what's alive, not what ever existed. An all-time total would climb
// forever and describe nothing: a panel installed once and deleted is not an
// install.
func TestPublicCountIgnoresLongDeadInstalls(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	s.setSetting(ctx, "beacon_receiver_enabled", "1")
	s.setSetting(ctx, "beacon_public_count_enabled", "1")
	s.setSetting(ctx, "beacon_public_count_min", "1")

	seedPings(t, s, 5, 40) // 5 alive, 40 long gone
	s.invalidatePublicCount()
	_, pc := getCount(t, s)
	if pc.Count == nil {
		t.Fatal("published nothing")
	}
	if *pc.Count != 5 {
		t.Errorf("published %d — that's the all-time total, not what's running", *pc.Count)
	}
}

// The publish settings have to survive a real PUT. Setting them directly in a
// test skips the JSON tags entirely, which is how a frontend sending the wrong
// key gets all the way to a running panel unnoticed.
func TestPublicCountSettingsRoundTripThroughPUT(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()

	body := `{"receiver_enabled":true,"public_count_enabled":true,"public_count_min":3}`
	req := httptest.NewRequest(http.MethodPut, "/api/settings/beacon", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleSetBeaconSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d — %s", w.Code, w.Body.String())
	}
	var got map[string]any
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["public_count_enabled"] != true {
		t.Errorf("PUT public_count_enabled:true came back %v — the key didn't bind", got["public_count_enabled"])
	}
	if got["public_count_min"] != float64(3) {
		t.Errorf("public_count_min came back %v, want 3", got["public_count_min"])
	}
	// And the endpoint it gates now actually answers.
	if !s.publicCountEnabled(ctx) {
		t.Error("PUT reported it on, but the setting reads off")
	}
}

// A floor of zero would publish any number at all, which is the one thing the
// setting exists to prevent.
func TestPublicCountMinRejectsZero(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	s.setSetting(ctx, "beacon_public_count_min", "0")
	if got := s.publicCountMin(ctx); got != defaultPublicCountMin {
		t.Errorf("a floor of 0 gave %d, want the default %d", got, defaultPublicCountMin)
	}
	s.setSetting(ctx, "beacon_public_count_min", "nonsense")
	if got := s.publicCountMin(ctx); got != defaultPublicCountMin {
		t.Errorf("an unparseable floor gave %d, want the default %d", got, defaultPublicCountMin)
	}
}
