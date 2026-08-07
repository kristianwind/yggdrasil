package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

type activityResp struct {
	Items []struct {
		TS       string `json:"ts"`
		Kind     string `json:"kind"`
		Class    string `json:"class"`
		ServerID string `json:"server_id"`
		Server   string `json:"server"`
		Title    string `json:"title"`
		Detail   string `json:"detail"`
		Actor    string `json:"actor"`
		Paged    bool   `json:"paged"`
	} `json:"items"`
	Hours int `json:"hours"`
}

func fleetActivity(t *testing.T, s *Server, query string) activityResp {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleFleetActivity(w, httptest.NewRequest(http.MethodGet, "/api/fleet/activity"+query, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got activityResp
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// seedServer inserts a server row so the feed has a name to join against.
func seedServer(t *testing.T, s *Server, name string) string {
	t.Helper()
	id := uuid.New().String()
	if _, err := s.db.Exec(
		`INSERT INTO servers (id, name, gameskill_id, status, data_dir) VALUES (?,?,?,'running',?)`,
		id, name, "wordpress", "/tmp/"+id); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	return id
}

// The four sources have to sort against each other, and that is exactly what a
// naive implementation gets wrong: audit_log stores RFC3339 ("…T…Z") while the
// other tables use SQLite's "YYYY-MM-DD HH:MM:SS". Compared as raw strings a
// space sorts before a T, so every crash would sink below every audit row from
// the same day no matter what the clock said. This test pins the merged order.
func TestFleetActivityMergesSourcesInTimeOrder(t *testing.T) {
	s := testServer(t)
	id := seedServer(t, s, "garageristeriet.dk")

	// Deliberately inserted out of order, and in the two different timestamp
	// formats the real tables use.
	s.db.Exec(`INSERT INTO server_crashes (server_id, ts, exit_code) VALUES (?, '2026-08-06 10:00:00', 137)`, id)
	s.db.Exec(`INSERT INTO audit_log (id, ts, action, username, resource) VALUES (?,?,?,?,?)`,
		uuid.New().String(), "2026-08-06T11:00:00Z", "server.stop", "admin", "server:"+id)
	s.db.Exec(`INSERT INTO alerts (id, server_id, key, class, title, reason, sources, hits, paged, created_at)
	           VALUES (?,?,?,?,?,?,?,?,?,?)`,
		uuid.New().String(), id, "watcher:w1", "incident", "wp-login flood",
		"one source, traffic getting through", "203.0.113.9", 400, 1, "2026-08-06 12:00:00")

	got := fleetActivity(t, s, "?hours=720")
	if len(got.Items) != 3 {
		t.Fatalf("got %d items, want 3 (one per source)", len(got.Items))
	}
	// Newest first: alert 12:00 → audit 11:00 → crash 10:00.
	wantKinds := []string{"alert", "audit", "crash"}
	for i, want := range wantKinds {
		if got.Items[i].Kind != want {
			t.Errorf("item %d kind = %q, want %q — the merged timeline is out of order (timestamp formats?)",
				i, got.Items[i].Kind, want)
		}
	}
	if got.Items[0].Server != "garageristeriet.dk" {
		t.Errorf("server name = %q, want it joined from the servers table", got.Items[0].Server)
	}
	if !got.Items[0].Paged {
		t.Error("an alert that paged must say so — that is how a routine detection is told from one that woke someone")
	}
	if got.Items[2].Detail != "exit 137" {
		t.Errorf("crash detail = %q, want the exit code", got.Items[2].Detail)
	}
}

// Routine detections are recorded but never paged. They must still appear here:
// without them a quiet fleet and a detector that silently died look identical,
// which is the whole reason Lag 1 keeps suppressed rows.
func TestFleetActivityShowsRoutineDetections(t *testing.T) {
	s := testServer(t)
	id := seedServer(t, s, "executit.dk")
	s.db.Exec(`INSERT INTO alerts (id, server_id, key, class, title, reason, hits, paged, created_at)
	           VALUES (?,?,?,?,?,?,?,?,datetime('now'))`,
		uuid.New().String(), id, "watcher:w2", "routine", "scanner walk", "refused probes", 40, 0)

	got := fleetActivity(t, s, "")
	if len(got.Items) != 1 {
		t.Fatalf("got %d items, want the routine alert to be listed", len(got.Items))
	}
	if got.Items[0].Class != "routine" || got.Items[0].Paged {
		t.Errorf("class=%q paged=%v, want a routine alert marked as such and not paged",
			got.Items[0].Class, got.Items[0].Paged)
	}
}

// The window must actually exclude older rows, and it is the caller's only
// defence against pulling a month of audit log into the Dashboard.
func TestFleetActivityHonoursTheWindow(t *testing.T) {
	s := testServer(t)
	id := seedServer(t, s, "old.dk")
	s.db.Exec(`INSERT INTO server_crashes (server_id, ts, exit_code) VALUES (?, datetime('now','-40 hours'), 1)`, id)

	if got := fleetActivity(t, s, "?hours=24"); len(got.Items) != 0 {
		t.Errorf("got %d items, want none — a 40h-old crash is outside a 24h window", len(got.Items))
	}
	if got := fleetActivity(t, s, "?hours=72"); len(got.Items) != 1 {
		t.Errorf("got %d items, want the crash back inside a 72h window", len(got.Items))
	}
}

// An out-of-range or junk hours parameter falls back to the default rather than
// erroring or, worse, being used raw in the window expression.
func TestFleetActivityClampsHours(t *testing.T) {
	for _, q := range []string{"?hours=0", "?hours=-5", "?hours=99999", "?hours=abc", ""} {
		if got := fleetActivity(t, s0(t), q); got.Hours != 24 {
			t.Errorf("hours for %q = %d, want the 24h default", q, got.Hours)
		}
	}
	if got := fleetActivity(t, s0(t), "?hours=168"); got.Hours != 168 {
		t.Errorf("hours = %d, want 168 to be accepted", got.Hours)
	}
}

func s0(t *testing.T) *Server { t.Helper(); return testServer(t) }
