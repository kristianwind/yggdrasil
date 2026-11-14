package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/auth"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

func loadRune(t *testing.T, path string) *gameskill.Gameskill {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gs, err := gameskill.Parse(b)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return gs
}

// server.control lets a delegate edit variables and reinstall. The Minecraft
// rune's install script has VER="{{MC_VERSION}}" and runs as root.
func TestDelegateCannotWriteTheInstallScriptThroughAVariable(t *testing.T) {
	gs := loadRune(t, "../../builtin-runes/minecraft-java.yaml")
	if codeContexts(gs)["MC_VERSION"]&ctxDouble == 0 {
		t.Fatalf("MC_VERSION is no longer seen inside double quotes in the install script (contexts=%v) — this test is not testing what it says", codeContexts(gs)["MC_VERSION"])
	}
	refused := []map[string]string{
		{"MC_VERSION": `x"; cp /bin/sh /data/rootsh; chmod 4755 /data/rootsh; #`},
		{"MC_VERSION": "$(id)"},
		{"MC_VERSION": "`id`"},
		{"MC_VERSION": "1.21\nrm -rf /data"},
		{"MC_VERSION": `1.21\`},
		{"MC_VERSION": "{{RCON_PASSWORD}}"},
		{"NOT_A_SETTING": "anything"},
		{"LD_PRELOAD": "/data/evil.so"},
	}
	for _, env := range refused {
		if err := guardDelegateEnv(gs, env); err == nil {
			t.Errorf("accepted from a delegate: %q", env)
		}
	}
	allowed := []map[string]string{
		{"MC_VERSION": "1.21.4"},
		{"MC_VERSION": "latest"},
		{"MC_VERSION": ""},
		{"LEVEL_SEED": "-4172144997902289642"},
		{"LEVEL_SEED": "Kristian's seed, v2 (hard)"},
	}
	for _, env := range allowed {
		if err := guardDelegateEnv(gs, env); err != nil {
			t.Errorf("refused an ordinary value %q: %v", env, err)
		}
	}
}

func TestScanShellReadsQuoting(t *testing.T) {
	got := map[string]shellCtx{}
	scanShell(`A="{{DQ}}" # don't mind {{CM}}
B='{{SQ}}' ; echo {{BARE}} "x\"{{ESC}}" 'it'"'"'s' "{{AFTER}}"`, got)
	want := map[string]shellCtx{"DQ": ctxDouble, "CM": ctxBare, "SQ": ctxSingle, "BARE": ctxBare, "ESC": ctxDouble, "AFTER": ctxDouble}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %v, want %v", k, got[k], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %v", got)
	}
}

// The create form submits every field, defaults included. If a shipped rune's
// own default failed the delegate rule, a delegate could not create that server
// at all — the gate would be a deletion.
func TestEveryShippedRuneDefaultIsSomethingADelegateMaySubmit(t *testing.T) {
	var files []string
	for _, root := range []string{"../../builtin-runes", "../../community-runes"} {
		filepath.Walk(root, func(p string, _ os.FileInfo, err error) error { //nolint:errcheck
			if err == nil && strings.HasSuffix(p, ".yaml") {
				files = append(files, p)
			}
			return nil
		})
	}
	if len(files) < 60 {
		t.Fatalf("only %d rune files found", len(files))
	}
	guarded, runes := 0, 0
	for _, f := range files {
		b, _ := os.ReadFile(f)
		gs, err := gameskill.Parse(b)
		if err != nil {
			continue // not a rune (a repo index, say); other tests own parse errors
		}
		runes++
		ctxs := codeContexts(gs)
		for _, v := range gs.Variables {
			if ctxs[v.Key] != 0 && v.Type != "int" && v.Type != "bool" && v.Type != "select" {
				guarded++
			}
		}
		if err := guardDelegateEnv(gs, gameskill.DefaultEnv(gs)); err != nil {
			t.Errorf("%s: its own default is refused: %v", filepath.Base(f), err)
		}
	}
	t.Logf("%d runes, %d free-text variables that reach a script, image or uid", runes, guarded)
	if runes < 60 || guarded < 15 {
		t.Fatalf("%d runes parsed and %d variables guarded — too few for this to have looked at anything", runes, guarded)
	}
}

func TestDelegateEnvIsCheckedOnUpdateAndAnAdminIsNot(t *testing.T) {
	s := testServer(t)
	serverID, delegate := serverForDelegate(t, s)
	blob, err := os.ReadFile("../../builtin-runes/minecraft-java.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec("DELETE FROM gameskills WHERE id='minecraft-java'") //nolint:errcheck
	if _, err := s.db.Exec("INSERT INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('minecraft-java','Minecraft','games',1,?,1)", string(blob)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.loadRuntime(t.Context(), serverID); err != nil {
		t.Fatalf("the test server has no loadable rune, so the guard would be refusing for the wrong reason: %v", err)
	}
	evil := `{"env":{"MC_VERSION":"x\"; id; #"}}`
	if w := updateServerAs(t, s, serverID, delegate, evil); w.Code != 403 {
		t.Errorf("delegate injection: %d %s", w.Code, w.Body.String())
	}
	if w := updateServerAs(t, s, serverID, delegate, `{"env":{"SOMETHING_ELSE":"1"}}`); w.Code != 403 {
		t.Errorf("delegate undeclared key: %d %s", w.Code, w.Body.String())
	}
	if w := updateServerAs(t, s, serverID, delegate, `{"env":{"MC_VERSION":"1.21.4"}}`); w.Code != 200 {
		t.Errorf("delegate ordinary edit: %d %s", w.Code, w.Body.String())
	}
	var envJSON string
	s.db.QueryRow("SELECT env_json FROM servers WHERE id=?", serverID).Scan(&envJSON) //nolint:errcheck
	if !strings.Contains(envJSON, "1.21.4") || strings.Contains(envJSON, "SOMETHING_ELSE") || strings.Contains(envJSON, "; id;") {
		t.Errorf("stored env is %s", envJSON)
	}
	// The name is a template value too ({{SERVER_NAME}}); Rust's start command
	// has +server.hostname "{{SERVER_NAME}}".
	rust := loadRune(t, "../../community-runes/games/rust.yaml")
	if codeContexts(rust)["SERVER_NAME"] == 0 {
		t.Fatal("rust.yaml no longer templates SERVER_NAME into its start command — pick another rune for this check")
	}
	if guardDelegateEnv(rust, map[string]string{"SERVER_NAME": `x" ; id ; "`}) == nil {
		t.Error("a delegate may rename a Rust server into its own start command")
	}
	if err := guardDelegateEnv(rust, map[string]string{"SERVER_NAME": "Kristians Rust — vanilla (EU)"}); err != nil {
		t.Errorf("an ordinary name was refused: %v", err)
	}
	if w := updateServerAs(t, s, serverID, delegate, `{"env":{"SERVER_NAME":"x"}}`); w.Code != 403 {
		t.Errorf("delegate set SERVER_NAME as a variable: %d", w.Code)
	}
	if w := updateServerAs(t, s, serverID, delegate, `{"name":"Our survival world"}`); w.Code != 200 {
		t.Errorf("delegate rename: %d %s", w.Code, w.Body.String())
	}
	admin := &auth.Claims{UserID: "a1", Username: "admin", Role: "admin"}
	if w := updateServerAs(t, s, serverID, admin, evil); w.Code != 200 {
		t.Errorf("an admin was refused: %d %s", w.Code, w.Body.String())
	}
}

func createServerAs(t *testing.T, s *Server, c *auth.Claims, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/servers", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCreateServer(w, r.WithContext(withClaims(r.Context(), c)))
	return w
}

// handleUpdateServer and PUT …/ports have refused these from a delegate for a
// long time. Create accepted both, and a hostname given there reaches NPM and
// Cloudflare on first start — where the existing proxy host for it is deleted.
func TestDelegateCannotClaimAHostnameOrAPortByCreatingAServer(t *testing.T) {
	s := testServer(t)
	blob, err := os.ReadFile("../../builtin-runes/minecraft-java.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec("DELETE FROM gameskills WHERE id='minecraft-java'") //nolint:errcheck
	if _, err := s.db.Exec("INSERT INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('minecraft-java','Minecraft','games',1,?,1)", string(blob)); err != nil {
		t.Fatal(err)
	}
	uid := "u-creator"
	s.db.Exec("INSERT INTO users (id, username, password_hash, role) VALUES (?,?,?,'user')", uid, "creator", "x")                                                                 //nolint:errcheck
	s.db.Exec("INSERT INTO permissions (id, user_id, scope_type, scope_id, perms) VALUES ('p-c',?,'gameskill','minecraft-java','server.create,server.control,server.view')", uid) //nolint:errcheck
	c := &auth.Claims{UserID: uid, Username: "creator", Role: "user"}

	for name, body := range map[string]string{
		"hostname":       `{"name":"a","gameskill_id":"minecraft-java","subdomain":"shop.example.dk"}`,
		"port":           `{"name":"a","gameskill_id":"minecraft-java","ports":{"game":443}}`,
		"script":         `{"name":"a","gameskill_id":"minecraft-java","env":{"MC_VERSION":"$(id)"}}`,
		"undeclared key": `{"name":"a","gameskill_id":"minecraft-java","env":{"X":"1"}}`,
	} {
		w := createServerAs(t, s, c, body)
		if w.Code != 403 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "admin") {
			t.Errorf("%s was refused, but not by the rule under test: %s", name, w.Body.String())
		}
	}
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM servers").Scan(&n) //nolint:errcheck
	if n != 0 {
		t.Errorf("%d servers were created by refused requests", n)
	}
}
