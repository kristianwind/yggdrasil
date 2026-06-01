package api

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kristianwind/yggdrasil/internal/docker"
)

// dockerEphemeral builds options for a Steam container run with the persistent
// cache mounted at /steamcache (HOME points there so the sentry cache survives).
func dockerEphemeral(image string, env []string, script, cacheDir string) docker.EphemeralOptions {
	return docker.EphemeralOptions{
		Image:       image,
		Env:         env,
		Script:      script,
		ExtraMounts: map[string]string{cacheDir: "/steamcache"},
	}
}

// Steam authorization (one-time) for games that require an account that owns
// them (e.g. DayZ). The admin logs in once with username + password + Steam
// Guard code; SteamCMD's sentry/credential cache is persisted in a dedicated
// volume reused by all subsequent updates, so Steam Guard isn't re-triggered.
// The password and Guard code are never stored or logged.

const steamImage = "cm2network/steamcmd:latest"

// steamCacheDir is the host directory holding SteamCMD's persistent cache. It is
// mounted into the auth container and every Steam install/update container.
func (s *Server) steamCacheDir() string {
	dir := filepath.Join(filepath.Dir(s.cfg.Database.Path), "steam-cache")
	if err := os.MkdirAll(dir, 0777); err == nil {
		os.Chmod(dir, 0777) // the container's steam user must be able to write
	}
	return dir
}

func (s *Server) handleGetSteamAccount(w http.ResponseWriter, r *http.Request) {
	var username string
	var authorized int
	var at string
	err := s.db.QueryRowContext(r.Context(),
		"SELECT username, authorized, COALESCE(authorized_at,'') FROM steam_account WHERE id=1").
		Scan(&username, &authorized, &at)
	if err != nil {
		jsonOK(w, map[string]any{"configured": false})
		return
	}
	jsonOK(w, map[string]any{
		"configured": true, "username": username,
		"authorized": authorized == 1, "authorized_at": at,
	})
}

// handleSteamSendCode attempts a login WITHOUT a Guard code. For accounts with
// email Steam Guard this prompts Steam to email a code (and fails asking for
// it) — which is exactly what we want for step 1 of the two-step flow. If the
// account has no Guard (or it's cached) the login may already succeed.
func (s *Server) handleSteamSendCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Username == "" || req.Password == "" {
		jsonError(w, "username and password required", http.StatusBadRequest)
		return
	}
	cacheDir := s.steamCacheDir()
	env := []string{
		"HOME=/steamcache",
		"STEAM_USER=" + req.Username,
		"STEAM_PASS=" + req.Password,
	}
	script := `"${STEAMCMDDIR:-/home/steam/steamcmd}/steamcmd.sh" +login "$STEAM_USER" "$STEAM_PASS" +quit`

	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	s.docker.RunEphemeralOpts(ctx, dockerEphemeral(steamImage, env, script, cacheDir), &buf) //nolint:errcheck
	out := buf.String()

	switch {
	case strings.Contains(out, "Invalid Password"):
		jsonError(w, "invalid username or password", http.StatusBadGateway)
	case strings.Contains(out, "Logged in OK"):
		// No Guard needed — caller can authorize directly with an empty code.
		jsonOK(w, map[string]any{"status": "no_guard_needed"})
	default:
		// Steam Guard challenge issued → the code was emailed.
		jsonOK(w, map[string]any{"status": "code_sent"})
	}
}

func (s *Server) handleAuthorizeSteam(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username  string `json:"username"`
		Password  string `json:"password"`
		GuardCode string `json:"guard_code"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Username == "" || req.Password == "" {
		jsonError(w, "username and password required", http.StatusBadRequest)
		return
	}

	// Run an interactive-style login. Credentials go via env (not the command
	// string) and the container is removed immediately after.
	cacheDir := s.steamCacheDir()
	env := []string{
		"HOME=/steamcache",
		"STEAM_USER=" + req.Username,
		"STEAM_PASS=" + req.Password,
		"STEAM_GUARD=" + req.GuardCode,
	}
	script := `"${STEAMCMDDIR:-/home/steam/steamcmd}/steamcmd.sh" ` +
		`+login "$STEAM_USER" "$STEAM_PASS" "$STEAM_GUARD" +quit`

	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	runErr := s.docker.RunEphemeralOpts(ctx, dockerEphemeral(steamImage, env, script, cacheDir), &buf)

	// Inspect output for a successful login WITHOUT echoing it (it may contain
	// sensitive prompts). We only surface a sanitized status.
	out := buf.String()
	if runErr != nil || !strings.Contains(out, "Logged in OK") && !strings.Contains(out, "Waiting for client config") {
		msg := steamFailureReason(out)
		jsonError(w, "Steam login failed: "+msg, http.StatusBadGateway)
		return
	}

	// Persist the username + authorized flag (never the password).
	s.db.ExecContext(r.Context(), `
		INSERT INTO steam_account (id, username, authorized, authorized_at)
		VALUES (1, ?, 1, ?)
		ON CONFLICT(id) DO UPDATE SET username=excluded.username, authorized=1, authorized_at=excluded.authorized_at
	`, req.Username, time.Now().UTC().Format(time.RFC3339))
	s.auditLog(r, "steam.authorize", "steam:"+req.Username, nil)
	jsonOK(w, map[string]string{"status": "authorized", "username": req.Username})
}

func (s *Server) handleDeleteSteamAccount(w http.ResponseWriter, r *http.Request) {
	// Forget the account record. We deliberately do NOT delete the sentry cache
	// on disk — keeping it is what avoids re-verification if re-added.
	s.db.ExecContext(r.Context(), "DELETE FROM steam_account WHERE id=1")
	s.auditLog(r, "steam.forget", "steam:account", nil)
	jsonOK(w, map[string]string{"status": "removed"})
}

// authorizedSteamUser returns the stored authorized username, or "".
func (s *Server) authorizedSteamUser(ctx context.Context) string {
	var username string
	s.db.QueryRowContext(ctx,
		"SELECT username FROM steam_account WHERE id=1 AND authorized=1").Scan(&username)
	return username
}

func steamFailureReason(out string) string {
	switch {
	case strings.Contains(out, "Invalid Password"):
		return "invalid password"
	case strings.Contains(out, "two-factor") || strings.Contains(out, "Steam Guard"):
		return "Steam Guard code required or incorrect"
	case strings.Contains(out, "RateLimitExceeded"):
		return "rate limited by Steam; wait and retry"
	default:
		return "check the username, password, and Steam Guard code"
	}
}
