package api

import (
	"log"
	"net/http"
	"strings"
	"time"

	"context"
	"github.com/kristianwind/yggdrasil/internal/auth"
	"github.com/kristianwind/yggdrasil/internal/notify"
)

// resetTokenTTL is how long an emailed password-reset link stays valid. Short by
// design: it's a bearer credential to a root-equivalent account.
const resetTokenTTL = time.Hour

// handleForgotPassword starts a password reset. It is public and deliberately
// opaque: whatever the input, whether the account or SMTP exists, it returns the
// same generic 200 so it can't be used to enumerate usernames or email
// addresses. The email (if any) is sent on a background goroutine so response
// time doesn't reveal whether a message went out.
func (s *Server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	if !loginLimiter.allow(r.RemoteAddr) {
		jsonError(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	var req struct {
		Identifier string `json:"identifier"` // username or email
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	// The one response every path returns — success is not observable.
	generic := func() {
		jsonOK(w, map[string]string{"status": "ok"})
	}

	ident := strings.TrimSpace(req.Identifier)
	if ident == "" {
		generic()
		return
	}

	// Housekeeping: drop expired tokens so the table can't accumulate.
	s.db.ExecContext(r.Context(), "DELETE FROM password_reset_tokens WHERE expires_at <= datetime('now')")

	// Look up an enabled account that actually has an email on file. Matching
	// username or email lets people reset with whichever they remember.
	var userID, email string
	err := s.db.QueryRowContext(r.Context(),
		`SELECT id, email FROM users
		 WHERE disabled=0 AND email!='' AND (username=? OR lower(email)=lower(?))
		 LIMIT 1`, ident, ident).Scan(&userID, &email)
	if err != nil {
		generic() // no such account (or it has no email) — say nothing
		return
	}

	cfg, ok := s.smtpConfig(r.Context())
	if !ok {
		// No mailer configured: nothing we can deliver. Don't mint a token that
		// can never reach anyone, and don't reveal the missing config.
		log.Printf("password reset requested for %q but no SMTP is configured", ident)
		generic()
		return
	}

	token, hash, err := auth.GenerateResetToken()
	if err != nil {
		generic()
		return
	}
	// One outstanding token per user: issuing a new link invalidates older ones.
	s.db.ExecContext(r.Context(), "DELETE FROM password_reset_tokens WHERE user_id=?", userID)
	expires := time.Now().UTC().Add(resetTokenTTL).Format("2006-01-02 15:04:05")
	if _, err := s.db.ExecContext(r.Context(),
		"INSERT INTO password_reset_tokens (token_hash, user_id, expires_at) VALUES (?,?,?)",
		hash, userID, expires); err != nil {
		generic()
		return
	}

	link := s.resetLinkBase(r.Context(), r) + "/#/reset?token=" + token
	cfg.To = email
	body := "A password reset was requested for your Yggdrasil account.\r\n\r\n" +
		"Open this link to choose a new password (valid for 1 hour):\r\n\r\n" +
		link + "\r\n\r\n" +
		"If you didn't request this, you can ignore this email — your password stays unchanged."
	// Send off the request path: SMTP can be slow, and a per-account send-time
	// difference would otherwise leak whether the account exists.
	go func() {
		if err := notify.SendEmail(cfg, "Reset your Yggdrasil password", body); err != nil {
			log.Printf("password reset email to %q failed: %v", email, err)
		}
	}()
	generic()
}

// handleResetPassword completes a reset: it verifies the emailed token, sets the
// new password, and revokes every existing session for the account. Public, but
// the token is an unguessable single-use credential.
func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	if !loginLimiter.allow(r.RemoteAddr) {
		jsonError(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		jsonError(w, "invalid or expired reset link", http.StatusBadRequest)
		return
	}
	if err := validatePassword(req.Password); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	hash := auth.HashToken(strings.TrimSpace(req.Token))
	var userID, username string
	err := s.db.QueryRowContext(r.Context(),
		`SELECT t.user_id, u.username
		 FROM password_reset_tokens t JOIN users u ON u.id = t.user_id
		 WHERE t.token_hash=? AND t.expires_at > datetime('now')`, hash).Scan(&userID, &username)
	if err != nil {
		jsonError(w, "invalid or expired reset link", http.StatusBadRequest)
		return
	}

	pwHash, err := auth.HashPassword(req.Password)
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Set the password and bump token_version so any session the (possibly
	// compromised) old password still held is revoked.
	if _, err := s.db.ExecContext(r.Context(),
		"UPDATE users SET password_hash=?, token_version = COALESCE(token_version,0)+1 WHERE id=?",
		pwHash, userID); err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Burn this and any sibling tokens (single use), and clear the failed-login
	// lockout so the user can sign in with the new password right away.
	s.db.ExecContext(r.Context(), "DELETE FROM password_reset_tokens WHERE user_id=?", userID)
	loginAccountLock.reset(strings.ToLower(username))
	s.auditLog(r, "user.password_reset", "user:"+userID, map[string]string{"username": username})
	jsonOK(w, map[string]string{"status": "ok"})
}

// panelBaseURL reconstructs the origin the browser used to reach the panel, so
// an emailed reset link points back to the same address the user is already on.
// Honors X-Forwarded-Proto because the panel commonly sits behind a TLS-
// terminating reverse proxy (NPM / Cloudflare Tunnel).
func panelBaseURL(r *http.Request) string {
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// resetLinkBase decides which address a password-reset link points at.
//
// panelBaseURL reads the request's own Host header, which the client chooses.
// For the OAuth metadata that is right -- a client has to be told the address it
// actually reached. For an emailed link it is not: POST /api/auth/forgot is
// unauthenticated, so anyone who knows a username can send it with
// `Host: attacker.example` and have the panel mail the real user a working
// reset link pointing at the attacker.
//
// When the operator has configured a public hostname, that is used instead and
// the header is ignored. Without one there is nothing to check against, so the
// header still decides -- the limiter, the single-use one-hour token and the
// fact that a reset does not clear TOTP are what carry it then.
func (s *Server) resetLinkBase(ctx context.Context, r *http.Request) string {
	host := strings.TrimSpace(s.getSetting(ctx, "public_hostname"))
	if host == "" {
		return panelBaseURL(r)
	}
	if !strings.Contains(host, "://") {
		// Stored as a bare hostname. A scheme-less value would make the browser
		// read it as a path, which is the same mistake the site-address field in
		// the WordPress rune guards against.
		host = "https://" + host
	}
	return strings.TrimRight(host, "/")
}
