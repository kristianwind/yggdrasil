package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/docker"
)

// diagServer is testServer plus the two dependencies the diagnostics handler
// reads. docker.New only constructs a client — it doesn't dial — so this works
// on a machine with no daemon: Version() then fails and renders "unreachable",
// which is the path CI exercises.
func diagServer(t *testing.T) *Server {
	t.Helper()
	s := testServer(t)
	dc, err := docker.New("")
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	s.docker = dc
	s.cfg = &config.Config{}
	s.cfg.Database.Path = filepath.Join(t.TempDir(), "test.db")
	s.version = "v9.9.9"
	return s
}

func getDiagnostics(t *testing.T, s *Server) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleDiagnostics(w, httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// The point of the whole feature: an admin can paste this into a public issue
// without leaking themselves. Seed the install with things that would identify a
// person and assert none of them survive into the report.
//
// This is the test that has to keep passing. Everything else here is detail; if
// a later change starts dumping a struct instead of naming fields, this is what
// catches it before someone's home directory ends up on GitHub.
func TestDiagnosticsLeaksNothingIdentifying(t *testing.T) {
	s := diagServer(t)

	// Builtin: id is the catalogue slug, name is the display name. An admin who
	// renamed a builtin to something personal must not have that name escape.
	skillID := "minecraft-java"
	s.db.Exec("INSERT INTO gameskills (id, name, category, version, yaml_blob, builtin) VALUES (?,?,?,?,?,1)",
		skillID, "Kristians Minecraft", "games", 10, "x")

	// A rune the admin wrote, named after their employer.
	s.db.Exec("INSERT INTO gameskills (id, name, category, version, yaml_blob, builtin) VALUES (?,?,?,?,?,0)",
		"acme-corp-internal", "ACME internal", "apps", 3, "x")

	// A server named after them, living under their home directory.
	s.db.Exec(`INSERT INTO servers (id, name, gameskill_id, status, data_dir, env_json)
		VALUES (?,?,?,'running',?,?)`,
		uuid.New().String(), "Kristians privatserver", skillID,
		"/home/kristian/games/survival", `{"RCON_PASSWORD":"hunter2","DOMAIN":"minecraft.example.dk"}`)

	report, _ := getDiagnostics(t, s)["report"].(string)
	if report == "" {
		t.Fatal("empty report")
	}

	for _, secret := range []string{
		"Kristians privatserver", // server name
		"/home/kristian",         // path, and the account name in it
		"hunter2",                // a rune variable's value
		"minecraft.example.dk",   // a domain
		"acme-corp-internal",     // a custom rune's name
	} {
		if strings.Contains(report, secret) {
			t.Errorf("report leaks %q:\n%s", secret, report)
		}
	}
}

// The report is worthless if it says nothing. Assert the facts a maintainer
// actually needs to triage are present.
func TestDiagnosticsReportsWhatTriageNeeds(t *testing.T) {
	s := diagServer(t)

	skillID := "minecraft-java"
	s.db.Exec("INSERT INTO gameskills (id, name, category, version, yaml_blob, builtin) VALUES (?,?,?,?,?,1)",
		skillID, "Minecraft (Java)", "games", 10, "x")
	s.db.Exec("INSERT INTO gameskills (id, name, category, version, yaml_blob, builtin) VALUES (?,?,?,?,?,0)",
		"private-thing", "Private thing", "apps", 1, "x")
	for i, status := range []string{"running", "running", "stopped"} {
		s.db.Exec(`INSERT INTO servers (id, name, gameskill_id, status, data_dir)
			VALUES (?,?,?,?,?)`, uuid.New().String(), string(rune('a'+i)), skillID, status, "/tmp/x")
	}

	out := getDiagnostics(t, s)
	report := out["report"].(string)

	for _, want := range []string{
		"v9.9.9",                  // panel version
		"minecraft-java v10",      // the builtin rune and its version
		"(+1 custom, not listed)", // custom runes counted, never named
		"3 (2 running)",           // server counts
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}

	// version and host are returned separately so the UI can prefill the two
	// required fields of the issue form.
	if out["version"] != "v9.9.9" {
		t.Errorf("version = %v", out["version"])
	}
	if h, _ := out["host"].(string); h == "" {
		t.Error("host is empty")
	}
	if u, _ := out["url"].(string); !strings.Contains(u, "template=bug_report.yml") {
		t.Errorf("url doesn't point at the issue template: %q", u)
	}
}

// A fresh install has no runes and no servers. It should still produce a report
// rather than a blank or a crash — that's exactly when someone files "it won't
// start".
func TestDiagnosticsOnEmptyInstall(t *testing.T) {
	s := diagServer(t)
	report := getDiagnostics(t, s)["report"].(string)
	if !strings.Contains(report, "Runes     none") {
		t.Errorf("expected 'none' for runes:\n%s", report)
	}
	if !strings.Contains(report, "0 (0 running)") {
		t.Errorf("expected zero servers:\n%s", report)
	}
}
