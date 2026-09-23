package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/auth"
)

// The fleet endpoints answer about the whole panel, so they were written
// against `servers` directly — and that is the bug. A signed-in user with one
// server-scoped grant received every server's name, and from /api/fleet/players
// the names of the players on all of them. On a panel where servers are
// delegated to different households, that is other people's children.
//
// This asserts the BEHAVIOUR, not the shape of the code: the structural
// route-gating test can only see that a permission helper is reached, and a
// handler that calls it and ignores the answer would satisfy that just as well.
func seedTwoServers(t *testing.T, s *Server) (mine, theirs string) {
	t.Helper()
	mine, theirs = uuid.New().String(), uuid.New().String()
	for id, name := range map[string]string{mine: "mine-server", theirs: "their-server"} {
		if _, err := s.db.Exec(
			`INSERT INTO servers (id, name, gameskill_id, status, data_dir, installed, install_status)
			 VALUES (?,?,?,'running','/tmp/x',1,'done')`, id, name, "minecraft-java"); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	return mine, theirs
}

func delegateOn(t *testing.T, s *Server, serverID string) *auth.Claims {
	t.Helper()
	uid := uuid.New().String()
	if _, err := s.db.Exec(
		"INSERT INTO users (id, username, password_hash, role) VALUES (?,?,?,'user')",
		uid, "delegate-"+uid[:8], "x"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := s.db.Exec(
		"INSERT INTO permissions (id, user_id, scope_type, scope_id, perms) VALUES (?,?,'server',?,?)",
		uuid.New().String(), uid, serverID, "server.view,server.control"); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	return &auth.Claims{UserID: uid, Username: "delegate", Role: "user"}
}

func TestFleetPlayersHidesServersTheCallerCannotSee(t *testing.T) {
	s := testServer(t)
	mine, theirs := seedTwoServers(t, s)
	_ = theirs
	claims := delegateOn(t, s, mine)

	r := httptest.NewRequest("GET", "/api/fleet/players", nil)
	r = r.WithContext(context.WithValue(r.Context(), claimsKey, claims))
	w := httptest.NewRecorder()
	s.handleFleetPlayers(w, r)

	body := w.Body.String()
	if strings.Contains(body, "their-server") {
		t.Errorf("a delegate with one grant sees another server in the fleet roster:\n%s", body)
	}
	if !strings.Contains(body, "mine-server") {
		t.Errorf("the delegate's OWN server is missing — the filter is too strict, "+
			"which would hide the bug by hiding everything:\n%s", body)
	}
}

func TestFleetSummaryCountsOnlyVisibleServers(t *testing.T) {
	s := testServer(t)
	mine, _ := seedTwoServers(t, s)
	claims := delegateOn(t, s, mine)

	r := httptest.NewRequest("GET", "/api/fleet/summary", nil)
	r = r.WithContext(context.WithValue(r.Context(), claimsKey, claims))
	w := httptest.NewRecorder()
	s.handleFleetSummary(w, r)

	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	got, _ := out["servers"].(float64)
	if got != 1 {
		t.Errorf("fleet servers = %v for a delegate granted 1 of 2 servers, want 1 — "+
			"the count alone reveals how big the panel is", got)
	}
}

// An admin must still see everything: a filter that hides the fleet from its
// operator is not a fix, it is a different bug, and the tests above would pass
// just as happily on one.
func TestFleetStaysCompleteForAnAdmin(t *testing.T) {
	s := testServer(t)
	seedTwoServers(t, s)
	admin := &auth.Claims{UserID: "a1", Username: "admin", Role: "admin"}

	r := httptest.NewRequest("GET", "/api/fleet/players", nil)
	r = r.WithContext(context.WithValue(r.Context(), claimsKey, admin))
	w := httptest.NewRecorder()
	s.handleFleetPlayers(w, r)

	body := w.Body.String()
	for _, want := range []string{"mine-server", "their-server"} {
		if !strings.Contains(body, want) {
			t.Errorf("admin cannot see %q in the fleet roster:\n%s", want, body)
		}
	}
}
