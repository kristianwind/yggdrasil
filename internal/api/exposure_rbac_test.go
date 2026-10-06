package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/auth"
)

// Two settings on a server decide whether it is reachable from outside the
// house: auto_forward opens its ports on the router, and subdomain points a
// hostname at it. Both were writable with server.control.
//
// That matters here specifically because servers get delegated: a child with
// control of their own Minecraft server could open its ports to the internet,
// spending an opt-in the admin gave once and globally, with nothing recording
// that they had. And because a value containing a dot is a full domain rather
// than a subdomain, they could point the family shop's hostname at their own
// container -- the NPM path deletes the existing proxy host for a domain before
// creating its own.
func serverForDelegate(t *testing.T, s *Server) (serverID string, claims *auth.Claims) {
	t.Helper()
	serverID = uuid.New().String()
	if _, err := s.db.Exec(
		`INSERT INTO servers (id, name, gameskill_id, status, data_dir, installed, install_status, auto_forward, subdomain)
		 VALUES (?,?,?,'stopped','/tmp/x',1,'done',0,'')`,
		serverID, "delegated", "minecraft-java"); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	uid := uuid.New().String()
	if _, err := s.db.Exec(
		"INSERT INTO users (id, username, password_hash, role) VALUES (?,?,?,'user')",
		uid, "kid-"+uid[:8], "x"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := s.db.Exec(
		"INSERT INTO permissions (id, user_id, scope_type, scope_id, perms) VALUES (?,?,'server',?,?)",
		uuid.New().String(), uid, serverID, "server.view,server.control"); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	return serverID, &auth.Claims{UserID: uid, Username: "kid", Role: "user"}
}

func updateServerAs(t *testing.T, s *Server, serverID string, c *auth.Claims, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("PATCH", "/api/servers/"+serverID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", serverID)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
	ctx = withClaims(ctx, c)
	w := httptest.NewRecorder()
	s.handleUpdateServer(w, r.WithContext(ctx))
	return w
}

func TestADelegateCannotOpenPortsOrClaimAHostname(t *testing.T) {
	s := testServer(t)
	serverID, delegate := serverForDelegate(t, s)

	for _, tc := range []struct {
		name, body, column string
	}{
		{"opening the router", `{"auto_forward":true}`, "auto_forward"},
		{"claiming a hostname", `{"subdomain":"shop.example.com"}`, "subdomain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := updateServerAs(t, s, serverID, delegate, tc.body)
			if w.Code != 403 {
				t.Errorf("got %d, want 403 — %s", w.Code, w.Body.String())
			}
			// The status code is not the point; the database is.
			var got string
			s.db.QueryRow("SELECT COALESCE("+tc.column+",'') FROM servers WHERE id=?", serverID).Scan(&got)
			if got != "0" && got != "" {
				t.Errorf("%s = %q after a refused request — it was written anyway", tc.column, got)
			}
		})
	}
}

// The gate must not be so wide that a delegate can no longer do their job: a
// check that refuses everything also refuses the attack, and passes.
func TestADelegateCanStillChangeTheirOwnServer(t *testing.T) {
	s := testServer(t)
	serverID, delegate := serverForDelegate(t, s)

	if w := updateServerAs(t, s, serverID, delegate, `{"autostart":false}`); w.Code != 200 {
		t.Errorf("a delegate could not change autostart: %d %s", w.Code, w.Body.String())
	}
	// And sending the CURRENT value of a gated field is not a change, so it must
	// not be refused — otherwise any edit form that posts every field breaks.
	if w := updateServerAs(t, s, serverID, delegate, `{"auto_forward":false}`); w.Code != 200 {
		t.Errorf("posting the unchanged value of auto_forward was refused: %d %s", w.Code, w.Body.String())
	}
}

func TestAnAdminCanStillSetBoth(t *testing.T) {
	s := testServer(t)
	serverID, _ := serverForDelegate(t, s)
	admin := &auth.Claims{UserID: "a1", Username: "admin", Role: "admin"}

	if w := updateServerAs(t, s, serverID, admin, `{"auto_forward":true}`); w.Code != 200 {
		t.Errorf("admin refused auto_forward: %d %s", w.Code, w.Body.String())
	}
	if w := updateServerAs(t, s, serverID, admin, `{"subdomain":"shop.example.com"}`); w.Code != 200 {
		t.Errorf("admin refused subdomain: %d %s", w.Code, w.Body.String())
	}
}

// A hostname already pointing at another server must not be silently taken.
// The extra-routes handler has had this check since it was written; the primary
// subdomain field never did.
func TestAHostnameAlreadyInUseIsRefused(t *testing.T) {
	s := testServer(t)
	admin := &auth.Claims{UserID: "a1", Username: "admin", Role: "admin"}

	first, _ := serverForDelegate(t, s)
	if w := updateServerAs(t, s, first, admin, `{"subdomain":"shop.example.com"}`); w.Code != 200 {
		t.Fatalf("setting the first hostname: %d %s", w.Code, w.Body.String())
	}
	second, _ := serverForDelegate(t, s)
	w := updateServerAs(t, s, second, admin, `{"subdomain":"shop.example.com"}`)
	if w.Code != 409 {
		t.Errorf("got %d, want 409 — the shop's hostname was handed to another server: %s",
			w.Code, w.Body.String())
	}
	// ...but a server may keep its own, or every save of an unrelated field fails.
	if w := updateServerAs(t, s, first, admin, `{"subdomain":"shop.example.com"}`); w.Code != 200 {
		t.Errorf("a server could not keep its own hostname: %d %s", w.Code, w.Body.String())
	}
}
