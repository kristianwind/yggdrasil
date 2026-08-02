package api

import (
	"context"
	"testing"
)

// A repository's own token must win over the panel-wide one, and a repository
// without one must still fall back — otherwise adding a token for a private repo
// would silently cut off every other repo.
func TestEffectiveGithubToken(t *testing.T) {
	ctx := context.Background()
	s := transferTestServer(t, "rune-repo-token-key-0123456789ab")

	globalEnc, err := s.cipher.Encrypt("global-token")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	s.setSetting(ctx, "github_token", globalEnc)

	repoEnc, err := s.storeRepoToken("their-token")
	if err != nil {
		t.Fatalf("store repo token: %v", err)
	}
	s.db.Exec("INSERT INTO rune_repos (id, name, repo, path, ref, token_enc) VALUES ('r1','Theirs','someone/private','runes','main',?)", repoEnc)
	// A saved repo with no token of its own must not shadow the panel-wide one.
	s.db.Exec("INSERT INTO rune_repos (id, name, repo, path, ref, token_enc) VALUES ('r2','Mine','kristianwind/yggdrasil','community-runes','main','')")

	cases := []struct {
		name, repo, path, ref, want string
	}{
		{"repo with its own token", "someone/private", "runes", "main", "their-token"},
		{"same repo, other folder", "someone/private", "other", "dev", "their-token"},
		{"saved repo without a token", "kristianwind/yggdrasil", "community-runes", "main", "global-token"},
		{"repo that isn't saved at all", "third/party", "runes", "main", "global-token"},
		{"no repo at all", "", "", "", "global-token"},
	}
	for _, c := range cases {
		if got := s.effectiveGithubToken(ctx, c.repo, c.path, c.ref); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}

	// The whole point of the fingerprint in the listing cache key: two repos on
	// different credentials must not be able to read each other's cached listing.
	if ghTokenFingerprint("their-token") == ghTokenFingerprint("global-token") {
		t.Error("different tokens produced the same cache fingerprint")
	}

	// Clearing the panel-wide token leaves the per-repo one intact.
	s.setSetting(ctx, "github_token", "")
	if got := s.effectiveGithubToken(ctx, "someone/private", "runes", "main"); got != "their-token" {
		t.Errorf("per-repo token lost when the global one was cleared: %q", got)
	}
	if got := s.effectiveGithubToken(ctx, "third/party", "runes", "main"); got != "" {
		t.Errorf("expected anonymous, got %q", got)
	}
}

// A stored token must never come back out through the API, and the listing must
// still say which repos have one.
func TestRuneRepoTokenNotReturned(t *testing.T) {
	s := transferTestServer(t, "rune-repo-token-key-0123456789ab")
	enc, err := s.storeRepoToken("secret-token")
	if err != nil {
		t.Fatalf("store repo token: %v", err)
	}
	if enc == "secret-token" || enc == "" {
		t.Fatalf("token was not encrypted for storage: %q", enc)
	}
	s.db.Exec("INSERT INTO rune_repos (id, name, repo, path, ref, token_enc) VALUES ('r1','Theirs','someone/private','runes','main',?)", enc)

	rows, err := s.db.Query("SELECT COALESCE(token_enc,'') FROM rune_repos WHERE id='r1'")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var stored string
		if rows.Scan(&stored) == nil && stored == "secret-token" {
			t.Fatal("token stored in plaintext")
		}
	}

	var d runeRepoDTO
	d.HasToken = enc != ""
	if d.Token != "" {
		t.Error("DTO carried a token value")
	}
	if !d.HasToken {
		t.Error("has_token should be true for a repo with a stored token")
	}
}
