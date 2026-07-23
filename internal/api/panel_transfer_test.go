package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/kristianwind/yggdrasil/internal/crypto"
	"github.com/kristianwind/yggdrasil/internal/db"
)

// transferTestServer is a testServer with its own encryption key, so a
// source→target round-trip actually crosses a key boundary like a real
// host-to-host move.
func transferTestServer(t *testing.T, key string) *Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	cipher, err := crypto.New(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return &Server{db: database, cipher: cipher}
}

func TestServerTransferTailRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := transferTestServer(t, "source-key-0123456789abcdef-xyz")
	dst := transferTestServer(t, "target-key-fedcba9876543210-abc")

	src.db.Exec("INSERT INTO servers (id, name, gameskill_id, status, env_json, ports_json, data_dir) VALUES ('s1','Src','mc','stopped','{}','{}','/tmp/x')")
	src.db.Exec("INSERT INTO schedules (id, name, server_id, cron_expr, action, args_json, enabled) VALUES ('sc1','Nightly restart','s1','0 4 * * *','restart','{}',1)")
	src.db.Exec("INSERT INTO log_watchers (id, server_id, name, pattern, threshold, window_secs, action, enabled, source) VALUES ('w1','s1','OOM','OutOfMemory',1,300,'kvasir',1,'rune')")
	chanEnc, _ := src.cipher.Encrypt(`{"webhook":"https://discord/hook"}`)
	src.db.Exec("INSERT INTO notifications (id, type, config_enc, enabled, server_id) VALUES ('n1','discord',?,1,'s1')", chanEnc)

	var man transferManifest
	src.collectServerTail(ctx, "s1", &man)
	if len(man.Schedules) != 1 || len(man.Watchers) != 1 || len(man.Channels) != 1 {
		t.Fatalf("tail not collected: %+v", man)
	}
	if man.Channels[0].Config != `{"webhook":"https://discord/hook"}` {
		t.Fatalf("channel config not decrypted: %q", man.Channels[0].Config)
	}
	man.Subdomain = "myapp"

	dst.db.Exec("INSERT INTO servers (id, name, gameskill_id, status, env_json, ports_json, data_dir) VALUES ('d1','Dst','mc','stopped','{}','{}','/tmp/y')")
	restored, dropped := dst.restoreServerTail(ctx, "d1", &man)
	if dropped != "" {
		t.Fatalf("subdomain should be free on the target, got dropped=%q", dropped)
	}
	if len(restored) != 4 { // schedules, watchers, channels, subdomain
		t.Fatalf("expected 4 restore notes, got %v", restored)
	}
	var n int
	dst.db.QueryRow("SELECT COUNT(*) FROM schedules WHERE server_id='d1'").Scan(&n)
	if n != 1 {
		t.Fatal("schedule not restored")
	}
	var encCfg string
	dst.db.QueryRow("SELECT config_enc FROM notifications WHERE server_id='d1'").Scan(&encCfg)
	if plain, err := dst.cipher.Decrypt(encCfg); err != nil || plain != `{"webhook":"https://discord/hook"}` {
		t.Fatalf("channel not re-encrypted with the TARGET key: err=%v plain=%q", err, plain)
	}
	var sub string
	dst.db.QueryRow("SELECT subdomain FROM servers WHERE id='d1'").Scan(&sub)
	if sub != "myapp" {
		t.Fatalf("subdomain not applied: %q", sub)
	}

	// A second import with the same subdomain must drop it, not steal it.
	dst.db.Exec("INSERT INTO servers (id, name, gameskill_id, status, env_json, ports_json, data_dir) VALUES ('d2','Dst2','mc','stopped','{}','{}','/tmp/z')")
	_, dropped = dst.restoreServerTail(ctx, "d2", &man)
	if dropped != "myapp" {
		t.Fatalf("subdomain clash not reported: %q", dropped)
	}
}

func TestPanelImportMergesWithoutClobbering(t *testing.T) {
	ctx := context.Background()
	dst := transferTestServer(t, "target-key-fedcba9876543210-abc")

	// Existing state on the target that must survive untouched.
	dst.db.Exec("INSERT INTO users (id, username, password_hash, role) VALUES ('u-old','kw','KEEP-HASH','admin')")
	dst.db.Exec("INSERT INTO rune_repos (id, name, repo, path, ref) VALUES ('r1','mine','kw/runes','apps','main')")

	bundle := panelBundle{
		Version: 1,
		Channels: []panelChannel{
			{Type: "discord", Config: `{"webhook":"https://hook/1"}`, Enabled: 1},
		},
		Settings: []panelSetting{
			{Key: "steam_web_api_key", Value: "STEAMKEY", Enc: true},
			{Key: "beacon_instance_id", Value: "evil-overwrite"}, // not on the allowlist
		},
		RuneRepos: []runeRepoDTO{
			{Name: "mine", Repo: "kw/runes", Path: "apps", Ref: "main"}, // duplicate — skip
			{Name: "theirs", Repo: "other/runes", Path: "", Ref: "main"},
		},
		Users: []panelUser{
			{ID: "u-x", Username: "kw", PasswordHash: "EVIL", Role: "admin"}, // exists — skip
			{ID: "u-new", Username: "helper", PasswordHash: "H2", Role: "user",
				Permissions: []string{"server|srv-1|server.view"}},
		},
	}

	sum := dst.applyPanelBundle(ctx, bundle)
	if sum["channels_added"] != 1 || sum["rune_repos_added"] != 1 || sum["rune_repos_skipped"] != 1 ||
		sum["users_added"] != 1 || sum["users_skipped"] != 1 || sum["settings_rejected"] != 1 {
		t.Fatalf("unexpected summary: %v", sum)
	}
	// Existing user untouched.
	var hash string
	dst.db.QueryRow("SELECT password_hash FROM users WHERE username='kw'").Scan(&hash)
	if hash != "KEEP-HASH" {
		t.Fatalf("existing user clobbered: %q", hash)
	}
	// New user + permission landed.
	var perms string
	dst.db.QueryRow("SELECT perms FROM permissions WHERE user_id='u-new'").Scan(&perms)
	if perms != "server.view" {
		t.Fatalf("permissions not imported: %q", perms)
	}
	// Setting re-encrypted with the target key; rejected key never written.
	var stored string
	dst.db.QueryRow("SELECT value FROM app_settings WHERE key='steam_web_api_key'").Scan(&stored)
	if plain, err := dst.cipher.Decrypt(stored); err != nil || plain != "STEAMKEY" {
		t.Fatalf("setting not re-encrypted: err=%v plain=%q", err, plain)
	}
	var evil int
	dst.db.QueryRow("SELECT COUNT(*) FROM app_settings WHERE key='beacon_instance_id'").Scan(&evil)
	if evil != 0 {
		t.Fatal("non-allowlisted setting was written")
	}

	// Idempotent: importing the same bundle again adds nothing.
	sum2 := dst.applyPanelBundle(ctx, bundle)
	if sum2["channels_added"] != 0 || sum2["users_added"] != 0 || sum2["rune_repos_added"] != 0 {
		t.Fatalf("second import was not idempotent: %v", sum2)
	}
}

// TestPanelExportUsersNoDeadlock guards the single-connection trap: the test DB
// also caps the pool at one connection, so a nested permissions query inside the
// open users iterator hangs this test forever instead of passing.
func TestPanelExportUsersNoDeadlock(t *testing.T) {
	s := transferTestServer(t, "target-key-fedcba9876543210-abc")
	s.db.Exec("INSERT INTO users (id, username, password_hash, role) VALUES ('u1','a','h','admin')")
	s.db.Exec("INSERT INTO users (id, username, password_hash, role) VALUES ('u2','b','h','user')")
	s.db.Exec("INSERT INTO permissions (id, user_id, scope_type, scope_id, perms) VALUES ('p1','u2','server','s1','server.view')")

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/panel/export?include=users", nil)
		s.handlePanelExport(rec, req)
		done <- rec
	}()
	select {
	case rec := <-done:
		var b panelBundle
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
			t.Fatalf("bad export json: %v", err)
		}
		if len(b.Users) != 2 || len(b.Users[1].Permissions)+len(b.Users[0].Permissions) != 1 {
			t.Fatalf("users/permissions not exported: %+v", b.Users)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("export deadlocked on the single-connection pool")
	}
}
