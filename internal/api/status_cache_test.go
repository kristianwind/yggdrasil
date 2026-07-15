package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/auth"
)

// updateServerAsAdmin drives the real PUT /api/servers/{id} handler, so these
// tests exercise the wiring rather than the helper the wiring is supposed to
// call. Calling invalidateStatusCache directly from a test would pass whether or
// not the handler ever invokes it — which is the bug being fixed.
func updateServerAsAdmin(t *testing.T, s *Server, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, "/api/servers/"+id, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	ctx = withClaims(ctx, &auth.Claims{UserID: "admin-user", Username: "admin", Role: "admin"})
	r = r.WithContext(ctx)

	w := httptest.NewRecorder()
	s.handleUpdateServer(w, r)
	return w
}

func boardHas(t *testing.T, s *Server, name string) bool {
	t.Helper()
	var st publicStatusResponse
	blob, _ := json.Marshal(s.buildPublicStatus(context.Background()))
	json.Unmarshal(blob, &st)
	for _, sv := range st.Servers {
		if sv.Name == name {
			return true
		}
	}
	return false
}

// cachedBoardHas reads through the cache the way GET /api/status does, so a stale
// entry is visible instead of being papered over by a fresh rebuild.
func cachedBoardHas(t *testing.T, s *Server, name string) bool {
	t.Helper()
	s.statusMu.Lock()
	if s.statusCache == nil {
		b, _ := json.Marshal(s.buildPublicStatus(context.Background()))
		s.statusCache = b
	}
	cached := s.statusCache
	s.statusMu.Unlock()

	var st publicStatusResponse
	json.Unmarshal(cached, &st)
	for _, sv := range st.Servers {
		if sv.Name == name {
			return true
		}
	}
	return false
}

func seedSharedServer(t *testing.T, s *Server, name string, public int) string {
	t.Helper()
	id := uuid.New().String()
	_, err := s.db.Exec(
		"INSERT INTO servers (id, name, gameskill_id, data_dir, status, status_public) VALUES (?,?,?,?,'running',?)",
		id, name, "minecraft-java", t.TempDir(), public)
	if err != nil {
		t.Fatalf("seed server: %v", err)
	}
	return id
}

// Un-sharing a server must drop it from the public board immediately.
//
// invalidateStatusCache had exactly one caller — the status-page settings
// endpoint — even though its own comment promised a "per-server opt-in" shows up
// right away. The per-server write never invalidated, so an un-shared server
// stayed public until the 15s cache expired. For a privacy control, lagging is
// the wrong way to fail.
func TestUnsharingAServerDropsItFromTheBoardImmediately(t *testing.T) {
	s := testServer(t)
	s.setSetting(context.Background(), "status_page_enabled", "1")
	id := seedSharedServer(t, s, "public-srv", 1)

	if !cachedBoardHas(t, s, "public-srv") {
		t.Fatal("setup: the server should start out on the board (and now be cached)")
	}

	if w := updateServerAsAdmin(t, s, id, `{"status_public":false}`); w.Code >= 400 {
		t.Fatalf("update returned %d: %s", w.Code, w.Body.String())
	}

	if cachedBoardHas(t, s, "public-srv") {
		t.Error("an un-shared server is still on the public board — the handler didn't drop the cache")
	}
}

// And sharing one must show it immediately; a toggle that appears to do nothing
// for 15s reads as broken.
func TestSharingAServerAddsItToTheBoardImmediately(t *testing.T) {
	s := testServer(t)
	s.setSetting(context.Background(), "status_page_enabled", "1")
	id := seedSharedServer(t, s, "newly-shared", 0)

	if cachedBoardHas(t, s, "newly-shared") {
		t.Fatal("setup: a private server must not be on the board")
	}

	if w := updateServerAsAdmin(t, s, id, `{"status_public":true}`); w.Code >= 400 {
		t.Fatalf("update returned %d: %s", w.Code, w.Body.String())
	}

	if !cachedBoardHas(t, s, "newly-shared") {
		t.Error("a newly shared server is not on the board — the handler didn't drop the cache")
	}
	if !boardHas(t, s, "newly-shared") {
		t.Error("the server isn't in a freshly built board either — the write didn't land")
	}
}
