package api

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/scheduler"
)

// seedMessageServer creates a server row and a message template, returning both ids.
func seedMessageServer(t *testing.T, s *Server, body string) (serverID, templateID string) {
	t.Helper()
	serverID = uuid.New().String()
	if _, err := s.db.Exec(
		"INSERT INTO servers (id,name,gameskill_id,status,env_json,ports_json,data_dir) VALUES (?,'Asgard','none','stopped','{}','{}','/tmp/x')",
		serverID); err != nil {
		t.Fatal(err)
	}
	templateID = uuid.New().String()
	if _, err := s.db.Exec(
		"INSERT INTO message_templates (id,name,body,builtin) VALUES (?,'T',?,0)",
		templateID, body); err != nil {
		t.Fatal(err)
	}
	return serverID, templateID
}

// The seeded "Restart countdown" template is selectable in the editor, and the
// editor never sent a seconds value. Every known key was passed to Render as ""
// regardless, so "say Restarting in {{seconds}} seconds!" was broadcast to
// players as "say Restarting in  seconds!" — and logged as a success.
func TestMessageActionRefusesUnfilledPlaceholder(t *testing.T) {
	s := testServer(t)
	sid, tid := seedMessageServer(t, s, "say Restarting in {{seconds}} seconds!")

	status, detail := s.runAction(scheduler.ActionMessage, sid, map[string]string{"template_id": tid})

	if status != "error" {
		t.Errorf("status = %q, want error — a half-filled message must not reach players", status)
	}
	if !strings.Contains(detail, "seconds") {
		t.Errorf("detail = %q, want it to name the missing placeholder", detail)
	}
}

// The value the admin gives is substituted, and a filled template still sends.
func TestMessageActionRendersSuppliedValue(t *testing.T) {
	s := testServer(t)
	sid, tid := seedMessageServer(t, s, "say Restarting in {{seconds}} seconds!")

	status, detail := s.runAction(scheduler.ActionMessage, sid,
		map[string]string{"template_id": tid, "seconds": "30"})

	if status != "ok" {
		t.Fatalf("status = %q (%s), want ok", status, detail)
	}
	if !strings.Contains(detail, "Restarting in 30 seconds!") {
		t.Errorf("detail = %q, want the rendered message", detail)
	}
}

// server_name is filled by the panel, not the admin, so a template using only it
// needs no values at all and must not be refused.
func TestMessageActionFillsServerName(t *testing.T) {
	s := testServer(t)
	sid, tid := seedMessageServer(t, s, "say Backing up {{server_name}}.")

	status, detail := s.runAction(scheduler.ActionMessage, sid, map[string]string{"template_id": tid})

	if status != "ok" {
		t.Fatalf("status = %q (%s), want ok", status, detail)
	}
	if !strings.Contains(detail, "Backing up Asgard.") {
		t.Errorf("detail = %q, want the server name substituted", detail)
	}
}
