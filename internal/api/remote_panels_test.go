package api

import (
	"context"
	"strings"
	"testing"
)

func savedPanelServer(t *testing.T) *Server {
	t.Helper()
	return transferTestServer(t, "panel-key-0123456789abcdef-xyz")
}

// The token is a credential on another panel. It is encrypted at rest and the
// API answers has_token, never the value — the same contract as cf_api_token.
// A saved token nobody can read back is the difference between a convenience
// and a second copy of a secret lying around.
func TestSavedTokenIsEncryptedAndNeverReturned(t *testing.T) {
	ctx := context.Background()
	s := savedPanelServer(t)

	id := "p1"
	enc, err := s.cipher.Encrypt("ygg_secret-token-value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	s.db.Exec("INSERT INTO remote_panels (id,name,url,token_enc) VALUES (?,?,?,?)",
		id, "kw01", "http://100.80.130.8:8080", enc)

	// At rest: not the plaintext.
	var stored string
	s.db.QueryRow("SELECT token_enc FROM remote_panels WHERE id=?", id).Scan(&stored)
	if strings.Contains(stored, "secret-token-value") {
		t.Fatal("the token is stored in a readable form")
	}

	// In use: decrypted correctly.
	url, token, err := s.resolveRemote(ctx, id, "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if token != "ygg_secret-token-value" || url != "http://100.80.130.8:8080" {
		t.Errorf("resolve gave (%q, %q)", url, token)
	}
}

// A one-off pull to an address that was never saved has to keep working
// untouched. This is an addition to the flow, not a replacement for it.
func TestExplicitAddressStillWorks(t *testing.T) {
	ctx := context.Background()
	s := savedPanelServer(t)

	url, token, err := s.resolveRemote(ctx, "", "100.92.81.54:8080", "ygg_typed")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if url != "http://100.92.81.54:8080" || token != "ygg_typed" {
		t.Errorf("got (%q, %q) — a bare host:port is the common tailnet case and must still normalise", url, token)
	}

	if _, _, err := s.resolveRemote(ctx, "", "100.92.81.54:8080", ""); err == nil {
		t.Error("an address with no token and no saved connection must fail, not proceed anonymously")
	}
}

// Saving the address without the token is a legitimate, lower-risk choice — it
// removes half the friction and stores no secret. Using such a connection has
// to say what is missing rather than fail obscurely.
func TestSavedPanelWithoutATokenAsksForOne(t *testing.T) {
	ctx := context.Background()
	s := savedPanelServer(t)
	s.db.Exec("INSERT INTO remote_panels (id,name,url,token_enc) VALUES ('p2','ovh','http://x:8080','')")

	_, _, err := s.resolveRemote(ctx, "p2", "", "")
	if err == nil {
		t.Fatal("want an error when no token is saved and none was supplied")
	}
	if !strings.Contains(err.Error(), "no token saved") {
		t.Errorf("the error must say what to do, got %q", err)
	}

	// ...and supplying one for that request works, without saving it.
	url, token, err := s.resolveRemote(ctx, "p2", "", "ygg_one-off")
	if err != nil || token != "ygg_one-off" || url != "http://x:8080" {
		t.Errorf("got (%q, %q, %v) — a typed token must override for this request", url, token, err)
	}
	var enc string
	s.db.QueryRow("SELECT token_enc FROM remote_panels WHERE id='p2'").Scan(&enc)
	if enc != "" {
		t.Error("using a token for one request must not silently save it")
	}
}

// Using a saved connection stamps it, so one nothing needs any more is visible
// rather than accumulating quietly.
func TestUsingASavedPanelStampsIt(t *testing.T) {
	ctx := context.Background()
	s := savedPanelServer(t)
	enc, _ := s.cipher.Encrypt("ygg_x")
	s.db.Exec("INSERT INTO remote_panels (id,name,url,token_enc) VALUES ('p3','prod','http://y:8080',?)", enc)

	var before string
	s.db.QueryRow("SELECT COALESCE(last_used_at,'') FROM remote_panels WHERE id='p3'").Scan(&before)
	if before != "" {
		t.Fatalf("precondition: want never used, got %q", before)
	}
	if _, _, err := s.resolveRemote(ctx, "p3", "", ""); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var after string
	s.db.QueryRow("SELECT COALESCE(last_used_at,'') FROM remote_panels WHERE id='p3'").Scan(&after)
	if after == "" {
		t.Error("last_used_at must be stamped on use")
	}
}

func TestUnknownSavedPanelIsRejected(t *testing.T) {
	if _, _, err := savedPanelServer(t).resolveRemote(context.Background(), "nope", "", ""); err == nil {
		t.Error("an unknown connection id must fail rather than fall back to an empty address")
	}
}
