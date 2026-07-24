package api

import (
	"context"
	"net"
	"testing"
)

func TestValidatePortChoice(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()

	if err := s.validatePortChoice(ctx, 0, nil); err == nil {
		t.Error("port 0 should be rejected")
	}
	if err := s.validatePortChoice(ctx, 70000, nil); err == nil {
		t.Error("port 70000 should be rejected")
	}
	// Already claimed earlier in this request.
	if err := s.validatePortChoice(ctx, 40000, map[int]bool{40000: true}); err == nil {
		t.Error("in-request taken port should be rejected")
	}
	// Recorded in port_allocations (held by another server).
	s.db.Exec("INSERT INTO port_allocations (port, server_id, name) VALUES (?,?,?)", 40001, "srv", "web")
	if err := s.validatePortChoice(ctx, 40001, nil); err == nil {
		t.Error("already-allocated port should be rejected")
	}
	// A genuinely free port validates (grab one the OS just handed us, then release).
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	free := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if err := s.validatePortChoice(ctx, free, nil); err != nil {
		t.Errorf("free port %d should validate: %v", free, err)
	}
}
