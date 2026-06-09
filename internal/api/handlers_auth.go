package api

import (
	"database/sql"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kristianwind/yggdrasil/internal/auth"
	"github.com/kristianwind/yggdrasil/internal/rbac"
	"github.com/kristianwind/yggdrasil/internal/totp"
)

// simple in-memory rate limiter: max 5 attempts per IP per minute
var loginLimiter = &rateLimiter{counts: make(map[string][]time.Time)}

type rateLimiter struct {
	mu        sync.Mutex
	counts    map[string][]time.Time
	lastSweep time.Time
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	window := now.Add(-time.Minute)
	// Periodically evict stale keys so a flood of spoofed X-Forwarded-For values
	// can't grow the map unbounded (memory DoS).
	if now.Sub(rl.lastSweep) > 2*time.Minute {
		for k, ts := range rl.counts {
			if len(ts) == 0 || ts[len(ts)-1].Before(window) {
				delete(rl.counts, k)
			}
		}
		rl.lastSweep = now
	}
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

// loginAccountLock enforces a per-account lockout, independent of source IP, so
// brute force can't be sidestepped by rotating X-Forwarded-For: after 10 failed
// attempts within 15 minutes a username is locked for 15 minutes.
var loginAccountLock = &accountLocker{fails: make(map[string]*acctFail)}

type acctFail struct {
	count int
	first time.Time
	until time.Time
}

type accountLocker struct {
	mu    sync.Mutex
	fails map[string]*acctFail
}

func (a *accountLocker) locked(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.fails[key]
	return f != nil && !f.until.IsZero() && time.Now().Before(f.until)
}

func (a *accountLocker) fail(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	f := a.fails[key]
	if f == nil || now.Sub(f.first) > 15*time.Minute {
		f = &acctFail{first: now}
		a.fails[key] = f
	}
	f.count++
	if f.count >= 10 {
		f.until = now.Add(15 * time.Minute)
	}
}

func (a *accountLocker) reset(key string) {
	a.mu.Lock()
	delete(a.fails, key)
	a.mu.Unlock()
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

	acctKey := strings.ToLower(strings.TrimSpace(req.Username))
	if loginAccountLock.locked(acctKey) {
		jsonError(w, "account temporarily locked due to repeated failed logins; try again later", http.StatusTooManyRequests)
		return
	}

	var userID, hash, role string
	var totpEnabled, tokenVer int
	var totpSecret sql.NullString
	err := s.db.QueryRow(
		"SELECT id, password_hash, role, totp_enabled, totp_secret, COALESCE(token_version,0) FROM users WHERE username=? AND disabled=0",
		req.Username,
	).Scan(&userID, &hash, &role, &totpEnabled, &totpSecret, &tokenVer)
	if err != nil {
		loginAccountLock.fail(acctKey)
		jsonError(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	ok, err := auth.VerifyPassword(req.Password, hash)
	if err != nil || !ok {
		loginAccountLock.fail(acctKey)
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
		ctr, valid := totp.ValidateAt(secret, req.Code)
		if derr != nil || !valid {
			loginAccountLock.fail(acctKey)
			jsonError(w, "invalid 2FA code", http.StatusUnauthorized)
			return
		}
		// Replay protection: reject a code (or earlier step) already accepted, so an
		// observed code can't be reused within its ±1-step validity window.
		var lastCtr int64
		s.db.QueryRow("SELECT COALESCE(totp_last_counter,0) FROM users WHERE id=?", userID).Scan(&lastCtr)
		if int64(ctr) <= lastCtr {
			loginAccountLock.fail(acctKey)
			jsonError(w, "2FA code already used; wait for the next one", http.StatusUnauthorized)
			return
		}
		s.db.Exec("UPDATE users SET totp_last_counter=? WHERE id=?", int64(ctr), userID)
	}

	loginAccountLock.reset(acctKey) // successful auth clears the failure counter
	token, err := auth.GenerateToken(userID, req.Username, role, tokenVer, s.cfg.Auth.SecretKey, s.cfg.Auth.SessionTTL)
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
	// Bump the user's token_version so every JWT issued to them is revoked server-side
	// (logout is otherwise only cosmetic — the bearer token stays valid until expiry).
	if c := claimsFromContext(r.Context()); c != nil && c.UserID != "" {
		s.db.ExecContext(r.Context(), "UPDATE users SET token_version = COALESCE(token_version,0)+1 WHERE id=?", c.UserID)
	}
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
	// can_create lets the UI show the "New server" button only to users who can
	// actually create one (admins, or a delegate with a create grant somewhere).
	canCreate := claims.Role == "admin"
	if !canCreate {
		canCreate = rbac.HasAny(s.loadGrants(r.Context(), claims.UserID), rbac.ServerCreate)
	}
	jsonOK(w, map[string]interface{}{
		"id":         claims.UserID,
		"username":   claims.Username,
		"role":       claims.Role,
		"can_create": canCreate,
	})
}
