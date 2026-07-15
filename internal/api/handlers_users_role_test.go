package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/auth"
)

func adminReq(t *testing.T, method, path, body, idParam string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	if idParam != "" {
		rctx.URLParams.Add("id", idParam)
	}
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	ctx = withClaims(ctx, &auth.Claims{UserID: "admin-user", Username: "admin", Role: "admin"})
	return r.WithContext(ctx)
}

func seedUser(t *testing.T, s *Server, name, role string) string {
	t.Helper()
	id := uuid.New().String()
	if _, err := s.db.Exec("INSERT INTO users (id, username, password_hash, role) VALUES (?,?,?,?)",
		id, name, "x", role); err != nil {
		t.Fatal(err)
	}
	return id
}

func roleOf(t *testing.T, s *Server, id string) string {
	t.Helper()
	var role string
	s.db.QueryRow("SELECT role FROM users WHERE id=?", id).Scan(&role)
	return role
}

// An unrecognised role used to fall through the update's condition and do
// nothing, while the request still returned 200 — so PUT {"role":"administrator"}
// read as a promotion that never happened.
func TestUpdateUserRejectsUnknownRole(t *testing.T) {
	s := testServer(t)
	id := seedUser(t, s, "delegate", "user")

	w := httptest.NewRecorder()
	s.handleUpdateUser(w, adminReq(t, http.MethodPut, "/api/users/"+id, `{"role":"administrator"}`, id))

	if w.Code != http.StatusBadRequest {
		t.Errorf("PUT with role=administrator returned %d, want 400 — it looked like it worked", w.Code)
	}
	if got := roleOf(t, s, id); got != "user" {
		t.Errorf("role is now %q, want it unchanged at \"user\"", got)
	}
}

// The valid roles must still apply.
func TestUpdateUserAcceptsKnownRoles(t *testing.T) {
	s := testServer(t)
	id := seedUser(t, s, "delegate", "user")

	for _, role := range []string{"admin", "user"} {
		w := httptest.NewRecorder()
		s.handleUpdateUser(w, adminReq(t, http.MethodPut, "/api/users/"+id, `{"role":"`+role+`"}`, id))
		if w.Code >= 400 {
			t.Fatalf("role=%s returned %d: %s", role, w.Code, w.Body.String())
		}
		if got := roleOf(t, s, id); got != role {
			t.Errorf("role = %q after setting %q", got, role)
		}
	}
}

// Rejecting the role must reject the whole request. The handler applies each
// field with its own UPDATE, so validating late would leave a new password set
// on a request that then 400s.
func TestUpdateUserRejectsBeforeApplyingAnything(t *testing.T) {
	s := testServer(t)
	id := seedUser(t, s, "delegate", "user")
	var before string
	s.db.QueryRow("SELECT password_hash FROM users WHERE id=?", id).Scan(&before)

	w := httptest.NewRecorder()
	s.handleUpdateUser(w, adminReq(t, http.MethodPut, "/api/users/"+id,
		`{"password":"a-new-password","role":"administrator"}`, id))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400", w.Code)
	}
	var after string
	s.db.QueryRow("SELECT password_hash FROM users WHERE id=?", id).Scan(&after)
	if after != before {
		t.Error("the password changed on a request that was rejected — the update applied partially")
	}
}

// Creating with no role at all still defaults to the safe one.
func TestCreateUserDefaultsToUserRole(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	s.handleCreateUser(w, adminReq(t, http.MethodPost, "/api/users", `{"username":"nobody","password":"pw123456"}`, ""))
	if w.Code >= 400 {
		t.Fatalf("create returned %d: %s", w.Code, w.Body.String())
	}
	var role string
	s.db.QueryRow("SELECT role FROM users WHERE username='nobody'").Scan(&role)
	if role != "user" {
		t.Errorf("role = %q, want the safe default \"user\"", role)
	}
}

// But naming a role we don't know is a typo, not a request for the default —
// creating "administrator" as a plain user is the kind of thing you notice only
// when the account can't do its job.
func TestCreateUserRejectsUnknownRole(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	s.handleCreateUser(w, adminReq(t, http.MethodPost, "/api/users",
		`{"username":"typo","password":"pw123456","role":"administrator"}`, ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("create with role=administrator returned %d, want 400", w.Code)
	}
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM users WHERE username='typo'").Scan(&n)
	if n != 0 {
		t.Error("the user was created anyway")
	}
}
