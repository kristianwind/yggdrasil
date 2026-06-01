package api

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestLoadRuntimePortInjection verifies loadRuntime exposes each allocated host
// port as <NAME>_PORT, so Steam games bind/advertise the actual external port.
func TestLoadRuntimePortInjection(t *testing.T) {
	s := testServer(t)
	yaml := "gameskill:\n" +
		"  id: testdz\n  name: TestDZ\n  docker: { image: x }\n" +
		"  steam: { app_id: 1, anonymous: false }\n" +
		"  startup: { command: \"run -port={{GAME_PORT}} -q={{QUERY_PORT}}\" }\n" +
		"  ports:\n    - { name: game, default: 2302, protocol: udp }\n    - { name: query, default: 27016, protocol: udp }\n"
	if _, err := s.db.Exec("INSERT INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('testdz','TestDZ','t',1,?,1)", yaml); err != nil {
		t.Fatal(err)
	}
	sid := uuid.New().String()
	if _, err := s.db.Exec(
		"INSERT INTO servers (id,name,gameskill_id,status,env_json,ports_json,data_dir) VALUES (?,?,?,?,?,?,?)",
		sid, "dz", "testdz", "stopped", "{}", `{"game":25000,"query":25001}`, "/tmp/x"); err != nil {
		t.Fatal(err)
	}
	rt, err := s.loadRuntime(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if rt.env["GAME_PORT"] != "25000" {
		t.Errorf("GAME_PORT=%q, want 25000", rt.env["GAME_PORT"])
	}
	if rt.env["QUERY_PORT"] != "25001" {
		t.Errorf("QUERY_PORT=%q, want 25001", rt.env["QUERY_PORT"])
	}
}
