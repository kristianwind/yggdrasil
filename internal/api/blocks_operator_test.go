package api

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The classifier no longer counts an operator's address as an attack source,
// but Kvasir reads raw log lines and can still name one in a proposal. So the
// guard also sits at the single point where a block is actually carried out —
// belt and braces, because the failure mode is locking the owner out of their
// own site, and with block_mode=auto nobody is asked first.

func seedAudit(t *testing.T, s *Server, ip, when string) {
	t.Helper()
	_, err := s.db.Exec(
		"INSERT INTO audit_log (id, username, action, resource, ip, ts) VALUES (?,?,?,?,?,datetime('now', ?))",
		uuid.New().String(), "admin", "auth.login", "session", ip, when)
	if err != nil {
		t.Fatalf("seed audit row: %v", err)
	}
}

func TestIsOperatorIP(t *testing.T) {
	s := testServer(t)
	seedAudit(t, s, "5.186.58.205", "-1 hour")
	seedAudit(t, s, "203.0.113.7", "-400 days") // long expired

	ctx := context.Background()
	if !s.isOperatorIP(ctx, "5.186.58.205") {
		t.Error("an address an admin signed in from an hour ago is one of ours")
	}
	if s.isOperatorIP(ctx, "203.0.113.7") {
		t.Error("a sign-in from over a year ago must not protect an address forever")
	}
	if s.isOperatorIP(ctx, "45.155.205.99") {
		t.Error("an address that never signed in is not an operator")
	}
	if s.isOperatorIP(ctx, "not-an-ip") {
		t.Error("garbage is not an operator address")
	}
}

// Written the same way the audit row is read back, so a padded or alternately
// spelled address can't slip past by string comparison alone.
func TestIsOperatorIPNormalisesTheAddress(t *testing.T) {
	s := testServer(t)
	seedAudit(t, s, " 5.186.58.205 ", "-1 hour")

	if !s.isOperatorIP(context.Background(), "5.186.58.205") {
		t.Error("surrounding whitespace in the audit row must not defeat the guard")
	}
}

func TestBlockIPRefusesAnOperatorAddress(t *testing.T) {
	s := testServer(t)
	seedAudit(t, s, "5.186.58.205", "-2 hours")
	s.db.Exec("INSERT INTO app_settings (key, value) VALUES ('block_enabled','1')")

	_, err := s.blockIP(context.Background(), "srv-1", "example.dk", "5.186.58.205", "scraping", "kvasir")
	if err == nil {
		t.Fatal("blocking an address an admin signs in from must be refused")
	}
	if !strings.Contains(err.Error(), "lock you out") {
		t.Errorf("the refusal should say why, got %q", err)
	}

	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM blocked_ips").Scan(&n)
	if n != 0 {
		t.Errorf("nothing should have been recorded, got %d rows", n)
	}
}

// The guard must not become a blanket refusal — a genuine attacker still gets
// blocked. (Blocking is disabled here, so this checks the guard let it through
// to the enable check rather than stopping it earlier.)
func TestBlockIPStillRefusesForTheRightReasonOnAStranger(t *testing.T) {
	s := testServer(t)
	seedAudit(t, s, "5.186.58.205", "-2 hours")

	_, err := s.blockIP(context.Background(), "srv-1", "example.dk", "45.155.205.99", "scanning", "kvasir")
	if err == nil {
		t.Fatal("expected an error: blocking is not enabled in this fixture")
	}
	if strings.Contains(err.Error(), "lock you out") {
		t.Errorf("a stranger's address must not hit the operator guard, got %q", err)
	}
}
