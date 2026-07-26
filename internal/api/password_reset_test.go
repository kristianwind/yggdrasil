package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/auth"
)

// seedUserWithToken creates a user (known password) plus a reset token expiring
// at the given time, and returns the user id and the plaintext token.
func seedUserWithToken(t *testing.T, s *Server, username, password string, expires time.Time) (string, string) {
	t.Helper()
	id := uuid.New().String()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := s.db.Exec(
		"INSERT INTO users (id, username, password_hash, email, role) VALUES (?,?,?,?,'admin')",
		id, username, hash, username+"@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	token, thash, err := auth.GenerateResetToken()
	if err != nil {
		t.Fatalf("gen token: %v", err)
	}
	if _, err := s.db.Exec(
		"INSERT INTO password_reset_tokens (token_hash, user_id, expires_at) VALUES (?,?,?)",
		thash, id, expires.UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	return id, token
}

func postReset(s *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/auth/reset", strings.NewReader(body))
	// Unique source per call: loginLimiter is a package-global (5/min/IP), so a
	// shared RemoteAddr across tests would spuriously trip the rate limit.
	req.RemoteAddr = uuid.NewString() + ":1"
	rec := httptest.NewRecorder()
	s.handleResetPassword(rec, req)
	return rec
}

func TestResetPasswordSuccess(t *testing.T) {
	s := testServer(t)
	id, token := seedUserWithToken(t, s, "alice", "old-password-1234", time.Now().Add(time.Hour))

	rec := postReset(s, `{"token":"`+token+`","password":"correct-horse-battery"}`)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// New password verifies, old one no longer does.
	var hash string
	var ver int
	s.db.QueryRow("SELECT password_hash, COALESCE(token_version,0) FROM users WHERE id=?", id).Scan(&hash, &ver)
	if ok, _ := auth.VerifyPassword("correct-horse-battery", hash); !ok {
		t.Error("new password should verify")
	}
	if ok, _ := auth.VerifyPassword("old-password-1234", hash); ok {
		t.Error("old password should no longer verify")
	}
	// token_version bumped → existing sessions revoked.
	if ver != 1 {
		t.Errorf("expected token_version 1, got %d", ver)
	}
	// Token is single-use: the row is gone.
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM password_reset_tokens WHERE user_id=?", id).Scan(&n)
	if n != 0 {
		t.Errorf("expected reset token consumed, %d remain", n)
	}
}

func TestResetPasswordRejectsUsedToken(t *testing.T) {
	s := testServer(t)
	_, token := seedUserWithToken(t, s, "bob", "old-password-1234", time.Now().Add(time.Hour))

	if rec := postReset(s, `{"token":"`+token+`","password":"correct-horse-battery"}`); rec.Code != 200 {
		t.Fatalf("first reset should succeed, got %d", rec.Code)
	}
	// Reusing the now-consumed token must fail.
	if rec := postReset(s, `{"token":"`+token+`","password":"another-valid-pass-9"}`); rec.Code == 200 {
		t.Errorf("reusing a consumed token should fail, got 200")
	}
}

func TestResetPasswordRejectsExpiredToken(t *testing.T) {
	s := testServer(t)
	id, token := seedUserWithToken(t, s, "carol", "old-password-1234", time.Now().Add(-time.Minute))

	rec := postReset(s, `{"token":"`+token+`","password":"correct-horse-battery"}`)
	if rec.Code == 200 {
		t.Fatalf("expired token should be rejected, got 200")
	}
	// Password must be unchanged.
	var hash string
	s.db.QueryRow("SELECT password_hash FROM users WHERE id=?", id).Scan(&hash)
	if ok, _ := auth.VerifyPassword("old-password-1234", hash); !ok {
		t.Error("password should be unchanged after a rejected reset")
	}
}

func TestResetPasswordRejectsWeakPassword(t *testing.T) {
	s := testServer(t)
	_, token := seedUserWithToken(t, s, "dave", "old-password-1234", time.Now().Add(time.Hour))

	// Too short (< 12) → 400, token preserved for a real retry.
	if rec := postReset(s, `{"token":"`+token+`","password":"short"}`); rec.Code != 400 {
		t.Errorf("weak password should be 400, got %d", rec.Code)
	}
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM password_reset_tokens").Scan(&n)
	if n != 1 {
		t.Errorf("token should survive a rejected weak password, %d remain", n)
	}
}

func TestForgotPasswordAlwaysGeneric(t *testing.T) {
	s := testServer(t)
	// Unknown identifier, no SMTP configured — must still be a plain 200 that
	// reveals nothing, and must not create a token.
	req := httptest.NewRequest("POST", "/api/auth/forgot", strings.NewReader(`{"identifier":"nobody"}`))
	req.RemoteAddr = uuid.NewString() + ":1"
	rec := httptest.NewRecorder()
	s.handleForgotPassword(rec, req)
	if rec.Code != 200 {
		t.Fatalf("forgot should always return 200, got %d", rec.Code)
	}
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM password_reset_tokens").Scan(&n)
	if n != 0 {
		t.Errorf("no token should be minted for an unknown user, got %d", n)
	}
}
