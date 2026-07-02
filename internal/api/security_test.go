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

func TestScheduleActionPerm(t *testing.T) {
	cases := map[scheduler.Action]struct {
		perm rbac.Permission
		need bool
	}{
		scheduler.ActionCommand: {rbac.ServerConsole, true},
		scheduler.ActionStart:   {rbac.ServerControl, true},
		scheduler.ActionStop:    {rbac.ServerControl, true},
		scheduler.ActionRestart: {rbac.ServerControl, true},
		scheduler.ActionUpdate:  {rbac.ServerControl, true},
		scheduler.ActionBackup:  {rbac.ServerBackup, true},
		scheduler.ActionMessage: {"", false},
	}
	for a, want := range cases {
		p, need := scheduleActionPerm(a)
		if need != want.need || p != want.perm {
			t.Errorf("action %q: got (%q,%v) want (%q,%v)", a, p, need, want.perm, want.need)
		}
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
