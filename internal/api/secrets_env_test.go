package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/auth"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// The near-misses matter as much as the hits. A substring match on "PASS" or
// "KEY" would mask BYPASS_CACHE and KEYBOARD_LAYOUT, which teaches an operator
// that the masking is noise and is how a real one gets ignored.
func TestLooksSecret(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"DB_PASSWORD", true},
		{"MARIADB_ROOT_PASSWORD", true},
		{"TUNNEL_TOKEN", true},
		{"ADMIN_TOKEN", true},
		{"NEXTAUTH_SECRET", true},
		{"MEILI_MASTER_KEY", true},
		{"OPENAI_API_KEY", true},
		{"FTLCONF_webserver_api_password", true}, // lower case, and a real one
		{"apiKey", true},                         // camelCase splits
		{"RCON_PASSWORD", true},
		{"PSK", true},

		{"KEYBOARD_LAYOUT", false}, // KEY is not a word here
		{"MONKEY_ISLAND", false},
		{"BYPASS_CACHE", false}, // PASS is not a word here
		{"COMPASS", false},
		{"SERVER_NAME", false},
		{"MC_VERSION", false},
		{"MAX_PLAYERS", false},
		{"", false},
	} {
		if got := looksSecret(tc.key); got != tc.want {
			t.Errorf("looksSecret(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// Every credential in every rune we ship has to be covered, by the flag or by
// the name. The count is printed because "all of them are covered" over zero
// variables and over 24 are the same green.
func TestEveryShippedRuneCredentialIsCovered(t *testing.T) {
	var files []string
	for _, root := range []string{"../../builtin-runes", "../../community-runes"} {
		if err := filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
			if err == nil && strings.HasSuffix(p, ".yaml") {
				files = append(files, p)
			}
			return nil
		}); err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	// Walked, not globbed: community-runes has apps/, databases/ and games/, and
	// a glob at one level reported 14 of the 22 as a complete survey.
	if len(files) < 60 {
		t.Fatalf("only %d rune files found — a check over a near-empty list is not a check", len(files))
	}

	var vars, covered, parsed int
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		gs, err := gameskill.Parse(b)
		if err != nil {
			t.Errorf("%s does not parse: %v", f, err)
			continue
		}
		parsed++
		keys := secretEnvKeys(gs)
		for _, v := range gs.Variables {
			if !looksSecret(v.Key) {
				continue
			}
			vars++
			if !keys[v.Key] {
				t.Errorf("%s: %s reads as a credential and is neither flagged nor floored —\n"+
					"it is stored in the clear and returned by GET /api/servers/{id}", gs.ID, v.Key)
				continue
			}
			covered++
		}
	}
	t.Logf("%d rune files parsed, %d credential-named variables, %d covered", parsed, vars, covered)
	if vars < 20 {
		t.Errorf("only %d credential-named variables found across %d runes — the detector got "+
			"narrower, and a shrinking denominator is how this check goes quiet", vars, parsed)
	}
}

// The behaviour, not the helper: a rune that never set secret:true still must
// not hand its password to anyone with server.view. This is the shape the bug
// actually had.
func TestServerResponseMasksACredentialTheRuneNeverFlagged(t *testing.T) {
	s := testServer(t)
	s.cfg = nil // loadRuntime is not reachable here; the floor must stand alone

	const yaml = `gameskill:
  id: leaky
  name: Leaky
  category: Apps
  version: 1
  docker:
    image: "nginx:alpine"
  variables:
    - { key: DB_PASSWORD, name: "Database password", type: string, default: "" }
    - { key: MAX_PLAYERS, name: "Players", type: int, default: 10 }
`
	if _, err := s.db.Exec(
		"INSERT INTO gameskills (id, name, category, version, yaml_blob, builtin) VALUES ('leaky','Leaky','Apps',1,?,0)",
		yaml); err != nil {
		t.Fatalf("seed rune: %v", err)
	}
	id := uuid.New().String()
	if _, err := s.db.Exec(
		`INSERT INTO servers (id, name, gameskill_id, status, data_dir, env_json, installed, install_status)
		 VALUES (?,?,?,'stopped','/tmp/x',?,1,'done')`,
		id, "leaky-1", "leaky",
		`{"DB_PASSWORD":"hunter2-the-real-one","MAX_PLAYERS":"10","STRAY_API_KEY":"sk-undeclared"}`,
	); err != nil {
		t.Fatalf("seed server: %v", err)
	}

	r := httptest.NewRequest("GET", "/api/servers/"+id, nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
	ctx = withClaims(ctx, &auth.Claims{UserID: "a1", Username: "admin", Role: "admin"})
	w := httptest.NewRecorder()
	s.handleGetServer(w, r.WithContext(ctx))

	if w.Code != 200 {
		t.Fatalf("get server: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "hunter2-the-real-one") {
		t.Errorf("the password came back in clear:\n%s", body)
	}
	// An env key the rune does not declare at all. A loop over the rune's
	// variables cannot see it, which is why the mask walks the env map.
	if strings.Contains(body, "sk-undeclared") {
		t.Errorf("an undeclared credential came back in clear:\n%s", body)
	}
	var out struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Env["MAX_PLAYERS"] != "10" {
		t.Errorf("MAX_PLAYERS = %q, want \"10\" — masking everything is not masking", out.Env["MAX_PLAYERS"])
	}
}
