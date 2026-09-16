package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/db"
)

// fixture writes a config pointing at a throwaway database with one user.
func fixture(t *testing.T, totpEnabled bool, passkeys int) (cfgPath, dbPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "test.db")
	cfgPath = filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("database:\n  path: "+dbPath+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	en := 0
	if totpEnabled {
		en = 1
	}
	if _, err := d.Exec(
		"INSERT INTO users (id, username, password_hash, role, totp_secret, totp_enabled) VALUES (?,?,?,?,?,?)",
		"u1", "kw", "x", "admin", "SECRET", en); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < passkeys; i++ {
		if _, err := d.Exec(
			"INSERT INTO webauthn_credentials (id, user_id, cred_id, cred_json, name) VALUES (?,?,?,?,?)",
			"c"+string(rune('a'+i)), "u1", "cred"+string(rune('a'+i)), "{}", "iPhone"); err != nil {
			t.Fatal(err)
		}
	}
	return cfgPath, dbPath
}

func state(t *testing.T, dbPath string) (secret sql.NullString, enabled, passkeys int) {
	t.Helper()
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.QueryRow("SELECT totp_secret, COALESCE(totp_enabled,0) FROM users WHERE id='u1'").
		Scan(&secret, &enabled); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow("SELECT COUNT(*) FROM webauthn_credentials WHERE user_id='u1'").Scan(&passkeys); err != nil {
		t.Fatal(err)
	}
	return
}

// The whole point: the dead secret goes away so a fresh one can be enrolled.
func TestResetTwoFactorClearsTheSecret(t *testing.T) {
	cfg, dbPath := fixture(t, true, 0)
	if err := runResetTwoFactor([]string{"kw", "--config", cfg}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	secret, enabled, _ := state(t, dbPath)
	if enabled != 0 {
		t.Error("2FA is still enabled")
	}
	if secret.Valid && secret.String != "" {
		t.Errorf("the old secret survived: %q — codes from the lost authenticator would still work", secret.String)
	}
}

// A passkey is a second factor in its own right and, in a lost-authenticator
// situation, usually the credential that still works. Clearing TOTP must not
// take it away as a side effect.
func TestResetTwoFactorLeavesPasskeysAlone(t *testing.T) {
	cfg, dbPath := fixture(t, true, 2)
	if err := runResetTwoFactor([]string{"kw", "--config", cfg}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, _, passkeys := state(t, dbPath); passkeys != 2 {
		t.Errorf("passkeys left = %d, want 2 — the still-working login was removed", passkeys)
	}
}

// "It said OK" on a mistyped username is how someone walks away believing they
// fixed an account they never touched.
func TestResetTwoFactorRefusesWhenThereIsNothingToReset(t *testing.T) {
	cfg, _ := fixture(t, false, 0)
	err := runResetTwoFactor([]string{"kw", "--config", cfg})
	if err == nil {
		t.Fatal("resetting an account without 2FA must fail loudly")
	}
	if !strings.Contains(err.Error(), "not enabled") {
		t.Errorf("the error should say why: %v", err)
	}
}

func TestResetTwoFactorUnknownUser(t *testing.T) {
	cfg, _ := fixture(t, true, 0)
	if err := runResetTwoFactor([]string{"nobody", "--config", cfg}); err == nil {
		t.Fatal("an unknown username must fail")
	}
}
