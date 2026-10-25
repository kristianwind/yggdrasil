package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/registry"
)

// The registry an image pulls from, the way Docker resolves it. A login is
// offered by this answer, so a wrong one here offers a credential to a stranger.
func TestRegistryOf(t *testing.T) {
	for ref, want := range map[string]string{
		"nginx":                              "docker.io",
		"nginx:alpine":                       "docker.io",
		"library/nginx":                      "docker.io",
		"docker.io/acme/app:1":               "docker.io",
		"index.docker.io/acme/app":           "docker.io",
		"ghcr.io/maziggy/bambuddy:latest":    "ghcr.io",
		"gitea.example.com/kw/cellarius:1.2": "gitea.example.com",
		"Gitea.Example.com/kw/cellarius":     "gitea.example.com", // the host part may be any case
		"localhost:5000/test/app":            "localhost:5000",
		"registry.lan:5005/team/app@sha256:" + strings.Repeat("a", 64): "registry.lan:5005",
		"{{IMAGE}}": "", // a placeholder left unfilled: no login
		"":          "",
		// A name whose first part looks like a host but is a Hub user stays Hub.
		"acme/app": "docker.io",
	} {
		if got := registryOf(ref); got != want {
			t.Errorf("registryOf(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestNormalizeRegistry(t *testing.T) {
	for in, want := range map[string]string{
		"gitea.example.com":                "gitea.example.com",
		"https://gitea.example.com/":       "gitea.example.com",
		"Gitea.Example.com":                "gitea.example.com",
		"gitea.example.com/kw/cellarius:1": "gitea.example.com",
		"ghcr.io":                          "ghcr.io",
		"https://hub.docker.com":           "docker.io",
		"index.docker.io":                  "docker.io",
		"localhost:5000":                   "localhost:5000",
	} {
		got, err := normalizeRegistry(in)
		if err != nil || got != want {
			t.Errorf("normalizeRegistry(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "  ", "not a host", "-bad.example.com"} {
		if _, err := normalizeRegistry(bad); err == nil {
			t.Errorf("normalizeRegistry(%q) accepted", bad)
		}
	}
}

// A login reaches the pull of an image on its registry, and no other.
func TestRegistryAuthFor(t *testing.T) {
	ctx := context.Background()
	s := transferTestServer(t, "registry-login-test-key-0123456789")

	save := func(reg, user, secret string) {
		body, _ := json.Marshal(map[string]string{"registry": reg, "username": user, "secret": secret})
		rec := httptest.NewRecorder()
		s.handleSaveRegistryLogin(rec, httptest.NewRequest("POST", "/", bytes.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("save %s: %d %s", reg, rec.Code, rec.Body)
		}
	}
	save("https://gitea.example.com/", "kw", "first-token")
	save("gitea.example.com", "kw", "gitea-token") // replaces, does not duplicate

	auth := s.registryAuthFor(ctx, "gitea.example.com/kw/cellarius:latest")
	if auth == "" {
		t.Fatal("no login offered for an image on the saved registry")
	}
	raw, err := base64.URLEncoding.DecodeString(auth)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var cfg registry.AuthConfig
	json.Unmarshal(raw, &cfg) //nolint:errcheck
	if cfg.Username != "kw" || cfg.Password != "gitea-token" || cfg.ServerAddress != "gitea.example.com" {
		t.Errorf("auth = %+v", cfg)
	}

	for _, ref := range []string{"nginx", "ghcr.io/kw/cellarius", "evil.example.net/gitea.example.com/x", "{{IMAGE}}"} {
		if s.registryAuthFor(ctx, ref) != "" {
			t.Errorf("a login was offered for %q, which is not on gitea.example.com", ref)
		}
	}

	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM registry_logins").Scan(&n) //nolint:errcheck
	if n != 1 {
		t.Errorf("%d rows after saving the same registry twice, want 1", n)
	}

	// The listing says a secret is stored and never what it is.
	rec := httptest.NewRecorder()
	s.handleListRegistryLogins(rec, httptest.NewRequest("GET", "/", nil))
	if strings.Contains(rec.Body.String(), "gitea-token") || !strings.Contains(rec.Body.String(), `"has_secret":true`) {
		t.Errorf("listing = %s", rec.Body)
	}
	// And the database holds it encrypted.
	var enc string
	s.db.QueryRow("SELECT secret_enc FROM registry_logins").Scan(&enc) //nolint:errcheck
	if enc == "" || strings.Contains(enc, "gitea-token") {
		t.Errorf("secret stored as %q", enc)
	}
}
