package auth

import "testing"

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Errorf("verify correct password: ok=%v err=%v", ok, err)
	}
	bad, _ := VerifyPassword("wrong password", hash)
	if bad {
		t.Error("verify wrong password returned true")
	}
}

func TestHashIsSalted(t *testing.T) {
	h1, _ := HashPassword("same")
	h2, _ := HashPassword("same")
	if h1 == h2 {
		t.Error("two hashes of the same password are identical (salt not applied)")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	const secret = "test-secret-key"
	tok, err := GenerateToken("uid-1", "alice", "admin", 0, secret, 1)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	claims, err := ParseToken(tok, secret)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.UserID != "uid-1" || claims.Username != "alice" || claims.Role != "admin" {
		t.Errorf("claims mismatch: %+v", claims)
	}
}

func TestTokenWrongSecretRejected(t *testing.T) {
	tok, _ := GenerateToken("uid-1", "alice", "admin", 0, "secret-a", 1)
	if _, err := ParseToken(tok, "secret-b"); err == nil {
		t.Error("token validated with wrong secret")
	}
}

func TestTokenExpired(t *testing.T) {
	tok, _ := GenerateToken("uid-1", "alice", "admin", 0, "secret", 0) // expires immediately
	if _, err := ParseToken(tok, "secret"); err == nil {
		t.Error("expired token accepted")
	}
}
