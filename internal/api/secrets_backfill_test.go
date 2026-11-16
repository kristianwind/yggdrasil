package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/crypto"
)

func storedEnv(t *testing.T, s *Server, id string) map[string]string {
	t.Helper()
	var j string
	if err := s.db.QueryRow("SELECT env_json FROM servers WHERE id=?", id).Scan(&j); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	json.Unmarshal([]byte(j), &env) //nolint:errcheck
	return env
}

// A server created before at-rest encryption, and never saved since, kept its
// passwords in plaintext for as long as nobody edited it. The boot pass has to
// encrypt them — declared or not — and leave every other value byte-identical.
func TestBootEncryptsCredentialsStillStoredInPlaintext(t *testing.T) {
	s := testServer(t)
	c, err := crypto.New("test-secret-key-abcdef-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	s.cipher = c
	blob, err := os.ReadFile("../../community-runes/apps/wordpress.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec("DELETE FROM gameskills WHERE id='wordpress'") //nolint:errcheck
	if _, err := s.db.Exec("INSERT INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES ('wordpress','WP','apps',1,?,0)", string(blob)); err != nil {
		t.Fatal(err)
	}
	already, _ := c.Encrypt("was-already-encrypted")
	legacy := map[string]string{
		"DB_PASSWORD":      "hunter2-db",
		"DB_ROOT_PASSWORD": "hunter2-root",
		"TUNNEL_TOKEN":     "undeclared-token", // not a wordpress variable at all
		"ADMIN_TOKEN":      already,
		"MAX_UPLOAD":       "64M",
		"BYPASS_CACHE":     "1", // "PASS" inside a word is not a credential
	}
	b, _ := json.Marshal(legacy)
	s.db.Exec("INSERT INTO servers (id,name,gameskill_id,data_dir,status,env_json) VALUES ('w1','shop','wordpress','/tmp/x','stopped',?)", string(b))          //nolint:errcheck
	s.db.Exec("INSERT INTO servers (id,name,gameskill_id,data_dir,status,env_json) VALUES ('g1','orphan','no-such-rune','/tmp/x','stopped',?)", `{"API_KEY":"k"}`) //nolint:errcheck

	n, v := s.encryptStoredSecrets(context.Background())
	if n != 2 || v != 4 {
		t.Errorf("encrypted %d values on %d servers, want 4 on 2", v, n)
	}
	env := storedEnv(t, s, "w1")
	for _, k := range []string{"DB_PASSWORD", "DB_ROOT_PASSWORD", "TUNNEL_TOKEN"} {
		if env[k] == legacy[k] {
			t.Errorf("%s is still plaintext", k)
		}
		if d, err := c.Decrypt(env[k]); err != nil || d != legacy[k] {
			t.Errorf("%s does not decrypt back to the original: %q %v", k, d, err)
		}
	}
	if env["ADMIN_TOKEN"] != already {
		t.Error("an already-encrypted value was encrypted again")
	}
	if env["MAX_UPLOAD"] != "64M" || env["BYPASS_CACHE"] != "1" {
		t.Errorf("a non-credential was touched: %v", env)
	}
	if o := storedEnv(t, s, "g1"); o["API_KEY"] == "k" {
		t.Error("a server whose rune is gone kept its credential in plaintext")
	}
	// Second boot: nothing to do.
	if n, v := s.encryptStoredSecrets(context.Background()); n != 0 || v != 0 {
		t.Errorf("second run rewrote %d values", v)
	}

	// And the server still starts with the real values: the runtime decrypts
	// the undeclared key as well as the declared ones.
	rt, err := s.loadRuntime(context.Background(), "w1")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"DB_PASSWORD", "TUNNEL_TOKEN", "ADMIN_TOKEN"} {
		want := legacy[k]
		if k == "ADMIN_TOKEN" {
			want = "was-already-encrypted"
		}
		if rt.env[k] != want {
			t.Errorf("runtime %s = %q, want %q", k, rt.env[k], want)
		}
	}
}

// The tunnel check read TUNNEL_TOKEN raw, so once the token was encrypted it
// compared ciphertext, found no tunnel id, and said nothing.
func TestTunnelCheckReadsAnEncryptedConnectorToken(t *testing.T) {
	s := testServer(t)
	c, _ := crypto.New("test-secret-key-abcdef-0123456789")
	s.cipher = c
	tok := base64.RawStdEncoding.EncodeToString([]byte(`{"a":"acct","t":"11111111-2222-3333-4444-555555555555","s":"c2VjcmV0"}`))
	enc, _ := c.Encrypt(tok)
	b, _ := json.Marshal(map[string]string{"TUNNEL_TOKEN": enc})
	s.db.Exec("INSERT INTO servers (id,name,gameskill_id,data_dir,status,env_json) VALUES ('cf','tunnel','cloudflared','/tmp/x','running',?)", string(b)) //nolint:errcheck
	s.setSetting(context.Background(), "cf_tunnel_id", "99999999-2222-3333-4444-555555555555")
	w := s.cfTunnelWarning(context.Background())
	if !strings.Contains(w, "11111111-2222-3333-4444-555555555555") {
		t.Fatalf("a connector on another tunnel went unreported: %q", w)
	}
}
