package api

import (
	"database/sql"
	"net/http"
	"sync"
	"time"

	"github.com/kristianwind/yggdrasil/internal/auth"
	"github.com/kristianwind/yggdrasil/internal/totp"
)

// simple in-memory rate limiter: max 5 attempts per IP per minute
var loginLimiter = &rateLimiter{counts: make(map[string][]time.Time)}

type rateLimiter struct {
	mu     sync.Mutex
	counts map[string][]time.Time
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	window := now.Add(-time.Minute)
	times := rl.counts[ip]

	// prune old entries
	valid := times[:0]
	for _, t := range times {
		if t.After(window) {
			valid = append(valid, t)
		}
	}
	if len(valid) >= 5 {
		rl.counts[ip] = valid
		return false
	}
	rl.counts[ip] = append(valid, now)
	return true
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if !loginLimiter.allow(ip) {
		jsonError(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"code"` // TOTP, when 2FA is enabled
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}

	var userID, hash, role string
	var totpEnabled int
	var totpSecret sql.NullString
	err := s.db.QueryRow(
		"SELECT id, password_hash, role, totp_enabled, totp_secret FROM users WHERE username=? AND disabled=0",
		req.Username,
	).Scan(&userID, &hash, &role, &totpEnabled, &totpSecret)
	if err != nil {
		jsonError(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	ok, err := auth.VerifyPassword(req.Password, hash)
	if err != nil || !ok {
		jsonError(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	// Second factor, when enabled.
	if totpEnabled == 1 {
		if req.Code == "" {
			jsonError(w, "2fa_required", http.StatusUnauthorized)
			return
		}
		secret, derr := s.cipher.Decrypt(totpSecret.String)
		if derr != nil || !totp.Validate(secret, req.Code) {
			jsonError(w, "invalid 2FA code", http.StatusUnauthorized)
			return
		}
	}

	token, err := auth.GenerateToken(userID, req.Username, role, s.cfg.Auth.SecretKey, s.cfg.Auth.SessionTTL)
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "ygg_token",
		Value:    token,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Path:     "/",
		MaxAge:   s.cfg.Auth.SessionTTL * 3600,
	})

	jsonOK(w, map[string]interface{}{
		"token":    token,
		"username": req.Username,
		"role":     role,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:   "ygg_token",
		Value:  "",
		MaxAge: -1,
		Path:   "/",
	})
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	jsonOK(w, map[string]interface{}{
		"id":       claims.UserID,
		"username": claims.Username,
		"role":     claims.Role,
	})
}
