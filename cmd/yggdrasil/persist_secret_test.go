package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/config"
)

// The shipped example has `secret_key: ""`. persistSecret saw the words
// "secret_key" and wrote nothing, so the panel made a new key on every start —
// and everything encrypted under the last one was gone.
func TestPersistSecretFillsInAnEmptyKey(t *testing.T) {
	example, err := os.ReadFile("../../deploy/config.yaml.example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(example), `secret_key: ""`) {
		t.Fatal("the example config no longer ships an empty secret_key — this test needs a new fixture")
	}
	for name, body := range map[string]string{
		"the shipped example": string(example),
		"bare":                "auth:\n  secret_key:\n  session_ttl: 24\n",
		"single quotes":       "auth:\n  secret_key: ''   # generated on first start\n",
		"no auth section":     "server:\n  port: 8080\n",
		"no file":             "",
	} {
		p := filepath.Join(t.TempDir(), "config.yaml")
		if name != "no file" {
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := persistSecret(p, "the-generated-key"); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		cfg, err := config.Load(p)
		if err != nil {
			t.Errorf("%s: the file no longer loads: %v", name, err)
			continue
		}
		if cfg.Auth.SecretKey != "the-generated-key" {
			after, _ := os.ReadFile(p)
			t.Errorf("%s: the next start would read key %q\n%s", name, cfg.Auth.SecretKey, after)
		}
	}
	// A key that is there must never be replaced.
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte("auth:\n  secret_key: \"keep-me\"\n"), 0o600) //nolint:errcheck
	if err := persistSecret(p, "other"); err == nil {
		t.Error("asked to persist over an existing key and reported success")
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "keep-me") || strings.Contains(string(b), "other") {
		t.Errorf("an existing key was changed: %s", b)
	}
}
