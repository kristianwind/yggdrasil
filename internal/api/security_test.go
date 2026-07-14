package api

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/crypto"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
	"github.com/kristianwind/yggdrasil/internal/rbac"
	"github.com/kristianwind/yggdrasil/internal/scheduler"
)

func TestSecretEnvEncryption(t *testing.T) {
	cipher, err := crypto.New("test-secret-key-at-least-16-chars-long")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cipher: cipher}
	gs := &gameskill.Gameskill{
		Variables: []gameskill.Variable{
			{Key: "RCON_PASSWORD", Secret: true},
			{Key: "API_KEY", Secret: true},
			{Key: "MEMORY_MB"},
		},
		RCON: &gameskill.RCON{PasswordVar: "RCON_PASSWORD"},
	}
	env := map[string]string{"RCON_PASSWORD": "hunter2", "API_KEY": "sk-abc", "MEMORY_MB": "4096"}

	s.encryptSecretEnv(env, gs)
	if env["RCON_PASSWORD"] == "hunter2" {
		t.Fatal("RCON password stored in plaintext at rest")
	}
	if env["API_KEY"] == "sk-abc" {
		t.Fatal("secret var API_KEY stored in plaintext")
	}
	if env["MEMORY_MB"] != "4096" {
		t.Fatalf("non-secret var altered: %q", env["MEMORY_MB"])
	}

	// Idempotent: re-encrypting (as the update-merge path does) must not
	// double-encrypt.
	enc := env["RCON_PASSWORD"]
	s.encryptSecretEnv(env, gs)
	if env["RCON_PASSWORD"] != enc {
		t.Fatal("double-encrypted on re-save")
	}

	// Round-trips back to the original values.
	s.decryptSecretEnv(env, gs)
	if env["RCON_PASSWORD"] != "hunter2" || env["API_KEY"] != "sk-abc" {
		t.Fatalf("decrypt did not round-trip: %v", env)
	}

	// Legacy plaintext (written before at-rest encryption) is left intact so it
	// still works and gets encrypted on the next save.
	legacy := map[string]string{"RCON_PASSWORD": "plainpw"}
	s.decryptSecretEnv(legacy, gs)
	if legacy["RCON_PASSWORD"] != "plainpw" {
		t.Fatalf("legacy plaintext mangled: %q", legacy["RCON_PASSWORD"])
	}
}

func TestSanitizeConsoleArg(t *testing.T) {
	// Newline-based command injection must be neutralized.
	if got := sanitizeConsoleArg("foo\nop attacker"); strings.ContainsAny(got, "\r\n") {
		t.Fatalf("newline survived sanitize: %q", got)
	}
	for _, in := range []string{"a\nb", "a\rb", "a\x00b", "a\x1bb"} {
		if strings.ContainsAny(sanitizeConsoleArg(in), "\r\n\x00\x1b") {
			t.Fatalf("control char survived for %q", in)
		}
	}
	if got := sanitizeConsoleArg("NormalName123 _-"); got != "NormalName123 _-" {
		t.Fatalf("benign value altered: %q", got)
	}
}

// TestScheduleActionPerm pins the extra permission each schedule action requires
// beyond server.schedule. A schedule must never be a way to run something the
// delegate can't trigger directly — scheduling a wipe requires server.control
// just like POST /api/servers/{id}/wipe does.
func TestScheduleActionPerm(t *testing.T) {
	// want[""] means "no extra permission beyond server.schedule".
	want := map[scheduler.Action]rbac.Permission{
		scheduler.ActionCommand: rbac.ServerConsole,
		scheduler.ActionStart:   rbac.ServerControl,
		scheduler.ActionStop:    rbac.ServerControl,
		scheduler.ActionRestart: rbac.ServerControl,
		scheduler.ActionUpdate:  rbac.ServerControl,
		scheduler.ActionWipe:    rbac.ServerControl,
		scheduler.ActionBackup:  rbac.ServerBackup,
		scheduler.ActionMessage: "",
	}
	// Exhaustive over AllActions: a newly-added action fails here until someone
	// decides what permission it needs, rather than silently requiring none.
	for _, a := range scheduler.AllActions {
		wp, ok := want[a]
		if !ok {
			t.Errorf("action %q has no expected permission — add it here and to scheduleActionPerms", a)
			continue
		}
		p, known := scheduleActionPerm(a)
		if !known || p != wp {
			t.Errorf("action %q: got (%q,known=%v) want (%q,known=true)", a, p, known, wp)
		}
	}
	for a := range want {
		if !scheduler.ValidAction(a) {
			t.Errorf("expectation names %q, which is not a valid action", a)
		}
	}
}

// TestScheduleActionPermFailsClosed covers the default: an action with no entry
// in the table must report known=false so the handler denies it, rather than
// reporting "no permission needed".
func TestScheduleActionPermFailsClosed(t *testing.T) {
	if p, known := scheduleActionPerm(scheduler.Action("nuke-from-orbit")); known {
		t.Errorf("unknown action reported known with perm %q; must fail closed", p)
	}
}

func TestRedactURI(t *testing.T) {
	u, _ := url.Parse("/api/servers/x/console?token=eyJsecret&foo=bar")
	got := redactURI(u)
	if strings.Contains(got, "eyJsecret") {
		t.Fatalf("token leaked in logged URI: %q", got)
	}
	if !strings.Contains(got, "foo=bar") {
		t.Fatalf("non-sensitive param dropped: %q", got)
	}
}

func TestValidateHostMountsSymlinkAndDenylist(t *testing.T) {
	// Canonicalize the temp root so the test is stable on platforms where the
	// temp dir itself sits behind a symlink (e.g. macOS /var -> /private/var).
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "ygg") // the panel's data dir (a denied source)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: &config.Config{Database: config.DatabaseConfig{Path: filepath.Join(dataDir, "ygg.db")}}}

	// A benign existing directory outside the denylist is accepted.
	good := filepath.Join(root, "media")
	os.MkdirAll(good, 0o755)
	if _, err := s.validateHostMounts([]hostMount{{Host: good, Container: "/media"}}); err != nil {
		t.Fatalf("benign mount rejected: %v", err)
	}

	// Direct mount of the configured data dir must be denied (dynamic denylist).
	if _, err := s.validateHostMounts([]hostMount{{Host: dataDir, Container: "/media"}}); err == nil {
		t.Fatal("configured data dir accepted as a mount source")
	}

	// A symlink whose real target is the (denied) data dir must not slip past
	// the string-level denylist — this is the EvalSymlinks bypass fix.
	link := filepath.Join(root, "sneaky")
	if err := os.Symlink(dataDir, link); err != nil {
		t.Skipf("cannot symlink: %v", err)
	}
	if _, err := s.validateHostMounts([]hostMount{{Host: link, Container: "/media"}}); err == nil {
		t.Fatal("symlink to the data dir was accepted — denylist bypass")
	}
}
