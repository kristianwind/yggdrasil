package api

import (
	"fmt"
	"strings"
	"testing"
)

// The gate is what turns "an attack is running" from a stream of messages into
// one. These tests use a real database because the dedupe lives in SQL — the
// cooldown has to survive a panel restart, so it cannot be in-memory state.

func incidentInput(key string) alertInput {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, accessLine("149.36.51.138", "/wp-login.php", "200"))
	}
	return alertInput{Key: key, Title: "Watcher wp-login", Hits: 20, Lines: lines}
}

func TestRaiseAlertPagesOnceThenSuppressesRepeats(t *testing.T) {
	s := testServer(t)

	v, page := s.raiseAlert("srv-1", incidentInput("watcher:w1"))
	if v.Class != alertIncident || !page {
		t.Fatalf("first sighting of an incident must page, got class=%q page=%v", v.Class, page)
	}
	// The same situation, still running, on the next 30-second scan tick.
	if _, page := s.raiseAlert("srv-1", incidentInput("watcher:w1")); page {
		t.Error("the same situation inside the cooldown must not page again — this is the flood the policy exists to stop")
	}
	// A different watcher is a different situation and is not suppressed.
	if _, page := s.raiseAlert("srv-1", incidentInput("watcher:w2")); !page {
		t.Error("a different situation must page on its own merits")
	}
	// Same watcher, different server: also its own situation.
	if _, page := s.raiseAlert("srv-2", incidentInput("watcher:w1")); !page {
		t.Error("the same watcher on another server is a separate situation")
	}

	var paged, total int
	s.db.QueryRow("SELECT COALESCE(SUM(paged),0), COUNT(*) FROM alerts").Scan(&paged, &total)
	if total != 4 {
		t.Errorf("every detection must be recorded, even suppressed ones: got %d rows, want 4", total)
	}
	if paged != 3 {
		t.Errorf("paged rows = %d, want 3", paged)
	}
}

func TestRaiseAlertRoutineNeverPagesButIsRecorded(t *testing.T) {
	s := testServer(t)

	lines := []string{
		accessLine("198.51.100.7", "/.env", "404"),
		accessLine("198.51.100.9", "/.git/config", "404"),
		accessLine("198.51.100.11", "/shell.php", "404"),
		accessLine("198.51.100.13", "/backup.sql", "404"),
		accessLine("198.51.100.15", "/hnap1/", "404"),
	}
	v, page := s.raiseAlert("srv-1", alertInput{Key: "watcher:scan", Title: "Watcher 404 flood", Hits: 5, Lines: lines})

	if v.Class != alertRoutine {
		t.Fatalf("scanner noise should be routine, got %q", v.Class)
	}
	if page {
		t.Error("routine situations must never page")
	}

	var class, reason string
	var pagedCol int
	s.db.QueryRow("SELECT class, reason, paged FROM alerts WHERE key='watcher:scan'").Scan(&class, &reason, &pagedCol)
	if class != "routine" || pagedCol != 0 {
		t.Errorf("recorded class=%q paged=%d, want routine/0", class, pagedCol)
	}
	if reason == "" {
		t.Error("the record must say why it was suppressed, or nobody can audit the policy")
	}
}

func TestRoutineAlertSummaryReportsWhatWasSuppressed(t *testing.T) {
	s := testServer(t)

	// Three sightings of one routine situation, plus one of another.
	noise := []string{
		accessLine("198.51.100.7", "/.env", "404"),
		accessLine("198.51.100.9", "/.git/config", "404"),
	}
	for i := 0; i < 3; i++ {
		s.raiseAlert("srv-1", alertInput{Key: "watcher:scan", Title: "Watcher 404 flood", Hits: 2, Lines: noise})
	}
	s.raiseAlert("srv-2", alertInput{Key: "watcher:probe", Title: "Watcher probe", Hits: 2, Lines: noise})

	got := s.routineAlertSummary(24)
	if !strings.Contains(got, "4 routine situations") {
		t.Errorf("summary should count every suppressed sighting, got:\n%s", got)
	}
	for _, want := range []string{"Watcher 404 flood", "3×", "Watcher probe"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q, got:\n%s", want, got)
		}
	}
}

// Silence must be distinguishable from a broken detector, so an empty summary
// means exactly "nothing was suppressed" and the digest can omit the section.
func TestRoutineAlertSummaryEmptyWhenNothingSuppressed(t *testing.T) {
	s := testServer(t)
	if got := s.routineAlertSummary(24); got != "" {
		t.Errorf("want empty summary with no alerts, got %q", got)
	}
	// A paged incident is not "handled quietly" and must not appear either.
	s.raiseAlert("srv-1", incidentInput("watcher:w1"))
	if got := s.routineAlertSummary(24); got != "" {
		t.Errorf("a paged incident must not show up as handled quietly, got %q", got)
	}
}

// The cooldown window is measured in SQL against created_at, so a row older
// than the window must stop suppressing.
func TestRaiseAlertCooldownExpires(t *testing.T) {
	s := testServer(t)

	if _, page := s.raiseAlert("srv-1", incidentInput("watcher:w1")); !page {
		t.Fatal("first alert should page")
	}
	if _, page := s.raiseAlert("srv-1", incidentInput("watcher:w1")); page {
		t.Fatal("second alert should be suppressed")
	}
	// Age the paged row past the cooldown.
	s.db.Exec("UPDATE alerts SET created_at = datetime('now', ?) WHERE paged=1",
		fmt.Sprintf("-%d minutes", int(alertPageCooldown.Minutes())+5))

	if _, page := s.raiseAlert("srv-1", incidentInput("watcher:w1")); !page {
		t.Error("once the cooldown has passed, an ongoing situation must be able to page again")
	}
}
