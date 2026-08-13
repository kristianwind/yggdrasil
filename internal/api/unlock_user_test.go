package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func lockOut(key string) {
	loginAccountLock.reset(key)
	for i := 0; i < 10; i++ { // the threshold is ten within fifteen minutes
		loginAccountLock.fail(key)
	}
}

func unlock(t *testing.T, s *Server, userID string) map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/users/"+userID+"/unlock", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", userID)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleUnlockUser(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out) //nolint:errcheck
	return out
}

// The case this exists for: someone mistyped their password ten times and now
// waits a quarter of an hour, and the only lever an admin had was restarting
// the panel — which drops every lock on the instance, including the ones
// holding an actual attack off.
func TestUnlockClearsOneAccount(t *testing.T) {
	s := testServer(t)
	id := seedUser(t, s, "kristian", "admin")
	seedUser(t, s, "someone-else", "admin")
	lockOut("kristian")
	lockOut("someone-else")

	if loginAccountLock.lockedUntil("kristian").IsZero() {
		t.Fatal("test setup: the account should be locked")
	}

	out := unlock(t, s, id)
	if out["was_locked"] != true {
		t.Errorf("was_locked = %v, want true", out["was_locked"])
	}
	if !loginAccountLock.lockedUntil("kristian").IsZero() {
		t.Error("the account is still locked after unlocking it")
	}
	// And only that one: the point of the endpoint is that it is not a restart.
	if loginAccountLock.lockedUntil("someone-else").IsZero() {
		t.Error("unlocking one account cleared another — that is the blunt behaviour this replaces")
	}
	loginAccountLock.reset("someone-else")
}

// Unlocking someone who was never locked is not an error; an admin should not
// have to check first, and the answer says which it was.
func TestUnlockIsHarmlessWhenNotLocked(t *testing.T) {
	s := testServer(t)
	id := seedUser(t, s, "calm", "admin")

	out := unlock(t, s, id)
	if out["was_locked"] != false {
		t.Errorf("was_locked = %v, want false", out["was_locked"])
	}
}

func TestUnlockUnknownUserIs404(t *testing.T) {
	s := testServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/users/nope/unlock", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "nope")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleUnlockUser(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// lockedUntil is what the admin list shows, so it has to say when they get back
// in rather than merely that they are out.
func TestLockedUntilReportsTheExpiry(t *testing.T) {
	loginAccountLock.reset("expiry-probe")
	defer loginAccountLock.reset("expiry-probe")

	if !loginAccountLock.lockedUntil("expiry-probe").IsZero() {
		t.Fatal("an untouched account must not report a lock")
	}
	// Nine failures is under the threshold: counted, not locked.
	for i := 0; i < 9; i++ {
		loginAccountLock.fail("expiry-probe")
	}
	if !loginAccountLock.lockedUntil("expiry-probe").IsZero() {
		t.Error("nine failures locked the account; the threshold is ten")
	}
	loginAccountLock.fail("expiry-probe")
	until := loginAccountLock.lockedUntil("expiry-probe")
	if until.IsZero() {
		t.Fatal("ten failures did not lock the account")
	}
	if d := time.Until(until); d <= 0 || d > 16*time.Minute {
		t.Errorf("lock expires in %v, want roughly fifteen minutes", d)
	}
}
