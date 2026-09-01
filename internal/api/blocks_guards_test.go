package api

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The case that motivated all of this. On 2026-08-01 Kvasir proposed blocking
// 198.41.192.37 twice — Cloudflare's own edge, inside 198.41.128.0/17. Blocking it
// would have dropped every proxied visitor to the site. The guard refused it, and
// the event log recorded the refusal with the same word as the eight legitimate
// suggestions, so nobody could tell it had happened.
func TestVetBlockRefusesCloudflaresOwnEdge(t *testing.T) {
	s := testServer(t)

	_, err := s.vetBlock(context.Background(), "198.41.192.37")
	if err == nil {
		t.Fatal("blocking Cloudflare's edge must be refused — it would drop all proxied traffic")
	}
	if !isRefusal(err) {
		t.Errorf("must be a refusal, not an operational failure: %v", err)
	}
	if !strings.Contains(err.Error(), "protected range") {
		t.Errorf("the message should say which guard objected, got: %v", err)
	}
}

// A refusal and a failure are different claims about the world: one says the
// detector was wrong, the other says the mechanism was. Recording both as
// "proposed" is what made the Cloudflare suggestion invisible.
func TestRefusalIsDistinguishableFromFailure(t *testing.T) {
	if !isRefusal(refuse("nope")) {
		t.Error("a guard refusal must be recognisable as one")
	}
	if isRefusal(context.DeadlineExceeded) {
		t.Error("an ordinary error must not be mistaken for a refusal")
	}
}

// The built-in ranges only cover what somebody thought of in advance. The
// allowlist is where the things only this install knows about go — starting with
// its own monitoring, which polls every site here every 60 seconds.
func TestAllowlistProtectsExactAddressAndRange(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	s.setSetting(ctx, "block_allowlist", "57.129.89.117\n203.0.113.0/24")

	for _, ip := range []string{"57.129.89.117", "203.0.113.42"} {
		if _, err := s.vetBlock(ctx, ip); err == nil {
			t.Errorf("%s is allowlisted and must be refused", ip)
		} else if !isRefusal(err) {
			t.Errorf("%s: must be a refusal, got %v", ip, err)
		} else if !strings.Contains(err.Error(), "allowlist") {
			t.Errorf("%s: the message should name the allowlist, got %v", ip, err)
		}
	}

	// A neighbour outside the range is still blockable — the allowlist must not
	// be quietly wider than it reads.
	if _, err := s.vetBlock(ctx, "203.0.114.42"); err != nil {
		t.Errorf("203.0.114.42 is outside the allowlisted range and should be blockable: %v", err)
	}
}

// An empty allowlist must not accidentally match everything, which is the classic
// way a parser that splits on separators fails open.
func TestEmptyAllowlistBlocksNothingFromBeingBlocked(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	for _, raw := range []string{"", "   ", "\n\n", ",,"} {
		s.setSetting(ctx, "block_allowlist", raw)
		if got := s.allowlistEntries(ctx); len(got) != 0 {
			t.Errorf("allowlist %q parsed to %v, want none", raw, got)
		}
		if _, err := s.vetBlock(ctx, "45.155.205.99"); err != nil {
			t.Errorf("allowlist %q must not protect an unrelated address: %v", raw, err)
		}
	}
}

// Kvasir's blocks lift themselves; a human's do not. That asymmetry is the whole
// argument for daring to turn auto on: a wrong block stops being wrong overnight,
// while a deliberate one stays until it is deliberately removed.
func TestOnlyAutomaticBlocksGetAnExpiry(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()

	auto, err := s.recordBlock(ctx, "", "45.155.205.99", "nftables", "", "", "scanning", "kvasir")
	if err != nil {
		t.Fatalf("record kvasir block: %v", err)
	}
	if auto.ExpiresAt == "" {
		t.Error("a Kvasir block must expire — without it auto mode is irreversible by design")
	}

	manual, err := s.recordBlock(ctx, "", "45.155.205.100", "nftables", "", "", "by hand", "manual")
	if err != nil {
		t.Fatalf("record manual block: %v", err)
	}
	if manual.ExpiresAt != "" {
		t.Errorf("a manual block must not expire, got %q", manual.ExpiresAt)
	}
}

// The selection is what the sweeper acts on: an automatic block past its time is
// due, a fresh one is not, and a manual block never is however old it gets.
//
// This tests the selection rather than the removal on purpose. Removal goes
// through nftables, which needs a privilege a test process does not have — a test
// that asserted on it would be measuring the sandbox, not this code.
func TestOnlyOverdueAutomaticBlocksAreDue(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()

	seed := func(ip, source, expires string) {
		t.Helper()
		if _, err := s.recordBlock(ctx, "", ip, "nftables", "", "", "seed", source); err != nil {
			t.Fatalf("seed %s: %v", ip, err)
		}
		if expires != "" {
			if _, err := s.db.Exec("UPDATE blocked_ips SET expires_at = datetime('now', ?) WHERE ip = ?", expires, ip); err != nil {
				t.Fatalf("backdate %s: %v", ip, err)
			}
		}
	}

	seed("45.155.205.99", "kvasir", "-1 hour")  // overdue
	seed("45.155.205.100", "kvasir", "+2 days") // still inside its TTL
	seed("45.155.205.101", "manual", "")        // a human said so; never expires

	due := map[string]bool{}
	for _, d := range s.blocksDue(ctx, time.Now()) {
		due[d.ip] = true
	}

	if !due["45.155.205.99"] {
		t.Error("an automatic block past its expiry must come up for removal")
	}
	if due["45.155.205.100"] {
		t.Error("an automatic block still inside its TTL must be left alone")
	}
	if due["45.155.205.101"] {
		t.Error("a manual block must never expire — the admin chose it deliberately")
	}
}
