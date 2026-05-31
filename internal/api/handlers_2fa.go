package api

import (
	"database/sql"
	"net/http"

	"github.com/kristianwind/yggdrasil/internal/totp"
)

// Two-factor authentication (TOTP). A user enrolls (setup → enable), after which
// login requires a 6-digit code. Secrets are encrypted at rest.

func (s *Server) handle2FAStatus(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	var enabled int
	s.db.QueryRowContext(r.Context(), "SELECT totp_enabled FROM users WHERE id=?", claims.UserID).Scan(&enabled)
	jsonOK(w, map[string]bool{"enabled": enabled == 1})
}

// handle2FASetup generates a fresh secret (stored pending, not yet enabled) and
// returns the secret + otpauth URI for the user's authenticator app.
func (s *Server) handle2FASetup(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	secret, err := totp.GenerateSecret()
	if err != nil {
		jsonError(w, "generate secret", http.StatusInternalServerError)
		return
	}
	enc, err := s.cipher.Encrypt(secret)
	if err != nil {
		jsonError(w, "encrypt", http.StatusInternalServerError)
		return
	}
	// Store as pending (secret set, enabled still 0).
	s.db.ExecContext(r.Context(), "UPDATE users SET totp_secret=?, totp_enabled=0 WHERE id=?", enc, claims.UserID)
	jsonOK(w, map[string]string{
		"secret": secret,
		"uri":    totp.URI(secret, claims.Username, "Yggdrasil"),
	})
}

// handle2FAEnable verifies a code against the pending secret and turns 2FA on.
func (s *Server) handle2FAEnable(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Code == "" {
		jsonError(w, "code required", http.StatusBadRequest)
		return
	}
	secret, ok := s.userTOTPSecret(r, claims.UserID)
	if !ok {
		jsonError(w, "run setup first", http.StatusBadRequest)
		return
	}
	if !totp.Validate(secret, req.Code) {
		jsonError(w, "invalid code", http.StatusBadRequest)
		return
	}
	s.db.ExecContext(r.Context(), "UPDATE users SET totp_enabled=1 WHERE id=?", claims.UserID)
	s.auditLog(r, "2fa.enable", "user:"+claims.UserID, nil)
	jsonOK(w, map[string]string{"status": "enabled"})
}

// handle2FADisable requires a valid code and turns 2FA off.
func (s *Server) handle2FADisable(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	secret, ok := s.userTOTPSecret(r, claims.UserID)
	if ok && !totp.Validate(secret, req.Code) {
		jsonError(w, "invalid code", http.StatusBadRequest)
		return
	}
	s.db.ExecContext(r.Context(), "UPDATE users SET totp_secret=NULL, totp_enabled=0 WHERE id=?", claims.UserID)
	s.auditLog(r, "2fa.disable", "user:"+claims.UserID, nil)
	jsonOK(w, map[string]string{"status": "disabled"})
}

func (s *Server) userTOTPSecret(r *http.Request, userID string) (string, bool) {
	var enc sql.NullString
	if err := s.db.QueryRowContext(r.Context(), "SELECT totp_secret FROM users WHERE id=?", userID).Scan(&enc); err != nil {
		return "", false
	}
	if !enc.Valid || enc.String == "" {
		return "", false
	}
	secret, err := s.cipher.Decrypt(enc.String)
	if err != nil {
		return "", false
	}
	return secret, true
}
