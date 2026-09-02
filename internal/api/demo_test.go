package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/config"
)

func demoServer(t *testing.T, on bool) http.Handler {
	t.Helper()
	s := &Server{cfg: &config.Config{}}
	s.cfg.Server.DemoMode = on
	return s.demoGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("reached"))
	}))
}

func do(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// The whole point: a stranger can look at everything and change nothing. The
// route list is long and grows, so this asserts the SHAPE — anything that is not
// a read is refused — rather than a list of paths that would rot.
func TestDemoRefusesEveryMutationItWasNotToldToAllow(t *testing.T) {
	h := demoServer(t, true)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/servers"},
		{http.MethodPost, "/api/servers/abc/start"},
		{http.MethodPost, "/api/servers/abc/stop"},
		{http.MethodDelete, "/api/servers/abc"},
		{http.MethodPut, "/api/servers/abc/ports"},
		{http.MethodPost, "/api/users"},
		{http.MethodPut, "/api/settings/analytics"},
		{http.MethodPost, "/api/system/prune-images"},
		{http.MethodPost, "/api/system/update"},
		{http.MethodPatch, "/api/anything"},
		{http.MethodDelete, "/api/servers/abc/routes/xyz"},
		// A route invented after this test was written must also be refused —
		// that is the property, not the list.
		{http.MethodPost, "/api/some/future/endpoint"},
	} {
		if got := do(h, tc.method, tc.path).Code; got != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", tc.method, tc.path, got)
		}
	}
}

// Signing in has to work or nobody sees anything.
func TestDemoAllowsSignIn(t *testing.T) {
	if got := do(demoServer(t, true), http.MethodPost, "/api/auth/login").Code; got != http.StatusOK {
		t.Errorf("login = %d, want 200", got)
	}
}

// Logout is refused on purpose: it revokes every session for the shared demo
// account, so one visitor pressing Sign out would eject everyone else. The web
// UI discards its local token regardless of the response, so the button still
// does the right thing for the person who pressed it.
func TestDemoRefusesLogoutSoOneVisitorCannotEjectTheRest(t *testing.T) {
	if got := do(demoServer(t, true), http.MethodPost, "/api/auth/logout").Code; got != http.StatusForbidden {
		t.Errorf("logout = %d, want 403", got)
	}
}

func TestDemoStillServesReads(t *testing.T) {
	h := demoServer(t, true)
	for _, p := range []string{"/api/servers", "/api/system/stats", "/api/version"} {
		if got := do(h, http.MethodGet, p).Code; got != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 — a demo that cannot be read is not a demo", p, got)
		}
	}
}

// Every install that is not a demo must be completely unaffected.
func TestGuardIsInertWhenDemoModeIsOff(t *testing.T) {
	h := demoServer(t, false)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/servers"},
		{http.MethodDelete, "/api/servers/abc"},
		{http.MethodPost, "/api/auth/logout"},
	} {
		if got := do(h, tc.method, tc.path).Code; got != http.StatusOK {
			t.Errorf("%s %s = %d with demo off, want 200", tc.method, tc.path, got)
		}
	}
}

// A trailing slash must not be a way around the guard.
func TestDemoGuardIsNotFooledByATrailingSlash(t *testing.T) {
	if got := do(demoServer(t, true), http.MethodPost, "/api/servers/").Code; got != http.StatusForbidden {
		t.Errorf("POST /api/servers/ = %d, want 403", got)
	}
}
