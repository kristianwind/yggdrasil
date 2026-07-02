package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func deleteRune(t *testing.T, s *Server, id string) int {
	t.Helper()
	r := httptest.NewRequest("DELETE", "/api/gameskills/"+id, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	s.handleDeleteGameskill(rr, r)
	return rr.Code
}

func TestDeleteGameskill(t *testing.T) {
	s := testServer(t)

	// A built-in default rune can be deleted, and the deletion is recorded so
	// seeding won't re-add it on the next boot.
	if _, err := s.db.Exec("INSERT INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('mc','MC','games',1,'x',1)"); err != nil {
		t.Fatal(err)
	}
	if code := deleteRune(t, s, "mc"); code != 200 {
		t.Fatalf("delete builtin: got %d, want 200", code)
	}
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM gameskills WHERE id='mc'").Scan(&n)
	if n != 0 {
		t.Fatal("built-in rune was not deleted")
	}
	s.db.QueryRow("SELECT COUNT(*) FROM deleted_builtins WHERE id='mc'").Scan(&n)
	if n != 1 {
		t.Fatal("built-in deletion was not recorded in deleted_builtins")
	}

	// A rune still referenced by a server cannot be deleted (409), so servers
	// are never orphaned.
	s.db.Exec("INSERT INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('rust','Rust','games',1,'x',0)")
	if _, err := s.db.Exec("INSERT INTO servers (id,name,gameskill_id,data_dir) VALUES ('s1','S','rust','/tmp/s1')"); err != nil {
		t.Fatal(err)
	}
	if code := deleteRune(t, s, "rust"); code != 409 {
		t.Fatalf("in-use rune delete: got %d, want 409", code)
	}
	s.db.QueryRow("SELECT COUNT(*) FROM gameskills WHERE id='rust'").Scan(&n)
	if n != 1 {
		t.Fatal("in-use rune was deleted despite the guard")
	}
}
