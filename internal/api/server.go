package api

import (
	"database/sql"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/kristianwind/yggdrasil/internal/auth"
	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/crypto"
	"github.com/kristianwind/yggdrasil/internal/docker"
)

type Server struct {
	cfg     *config.Config
	db      *sql.DB
	docker  *docker.Client
	router  *chi.Mux
	webFS   fs.FS
	install *progressHub // live install/build output, keyed by server id
	cipher  *crypto.Cipher
	sched   *schedulerState
	viol    *violationWatcher
	version string // build version (set via SetVersion)

	extIP   string // cached external IP (detectPublicAddr)
	extIPAt time.Time
	extIPMu sync.Mutex

	latestVer string // cached latest GitHub release tag
	latestAt  time.Time
	latestMu  sync.Mutex
}

// SetVersion records the build version so it can be surfaced in the UI.
func (s *Server) SetVersion(v string) { s.version = v }

func New(cfg *config.Config, db *sql.DB, dc *docker.Client, webFS embed.FS) *Server {
	subFS, _ := fs.Sub(webFS, "web/dist")
	cipher, _ := crypto.New(cfg.Auth.SecretKey)

	s := &Server{
		cfg:     cfg,
		db:      db,
		docker:  dc,
		webFS:   subFS,
		install: newProgressHub(),
		cipher:  cipher,
	}
	s.router = s.buildRouter()
	s.StartScheduler()
	s.viol = newViolationWatcher(s)
	s.viol.Start()
	s.startDiskMonitor()
	s.startStatusReconciler()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) buildRouter() *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	// NOTE: no global request timeout — WebSocket streams (console/logs/install)
	// are long-lived, and container operations (image pulls, server start) can
	// exceed a minute. A blanket timeout dropped those connections ("Failed to
	// fetch" on the client). Individual operations use their own contexts.
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		AllowCredentials: true,
	}))
	r.Use(secureHeaders)

	// Public routes
	r.Post("/api/auth/login", s.handleLogin)
	r.Get("/api/version", s.handleVersion)

	// Authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware)
		r.Post("/api/auth/logout", s.handleLogout)
		r.Get("/api/auth/me", s.handleMe)

		// Two-factor auth (TOTP)
		r.Get("/api/auth/2fa", s.handle2FAStatus)
		r.Post("/api/auth/2fa/setup", s.handle2FASetup)
		r.Post("/api/auth/2fa/enable", s.handle2FAEnable)
		r.Post("/api/auth/2fa/disable", s.handle2FADisable)

		// Gameskills (Runes)
		r.Get("/api/gameskills", s.handleListGameskills)
		r.Post("/api/gameskills", s.handleUploadGameskill)
		r.Post("/api/gameskills/import-egg", s.handleImportEgg)
		r.Post("/api/gameskills/import-xml", s.handleImportXML)
		r.Get("/api/gameskills/github", s.handleGithubRunes)
		r.Post("/api/gameskills/install-from-github", s.handleInstallGithubRune)
		r.Get("/api/gameskills/{id}", s.handleGetGameskill)
		r.Delete("/api/gameskills/{id}", s.handleDeleteGameskill)

		// API tokens (for automation)
		r.Get("/api/tokens", s.handleListTokens)
		r.Post("/api/tokens", s.handleCreateToken)
		r.Delete("/api/tokens/{id}", s.handleDeleteToken)

		// Servers
		r.Get("/api/servers", s.handleListServers)
		r.Post("/api/servers", s.handleCreateServer)
		r.Get("/api/servers/{id}", s.handleGetServer)
		r.Put("/api/servers/{id}", s.handleUpdateServer)
		r.Delete("/api/servers/{id}", s.handleDeleteServer)
		r.Post("/api/servers/{id}/install", s.handleInstallServer)
		r.Get("/api/servers/{id}/install/log", s.handleInstallLog) // WebSocket
		r.Post("/api/servers/{id}/start", s.handleStartServer)
		r.Post("/api/servers/{id}/stop", s.handleStopServer)
		r.Post("/api/servers/{id}/restart", s.handleRestartServer)
		r.Get("/api/servers/{id}/stats", s.handleServerStats)
		r.Get("/api/servers/{id}/query", s.handleServerQuery)
		r.Get("/api/servers/{id}/battlemetrics", s.handleServerBattleMetrics)
		r.Get("/api/servers/{id}/reachability", s.handleServerReachability)
		r.Get("/api/servers/{id}/dayz/economy", s.handleDayzEconomy)
		r.Get("/api/servers/{id}/dayz/mods", s.handleDayzMods)
		r.Post("/api/servers/{id}/dayz/min-lifetime", s.handleDayzMinLifetime)
		r.Post("/api/servers/{id}/dayz/globals", s.handleDayzGlobals)
		r.Post("/api/servers/{id}/dayz/register-types", s.handleDayzRegisterTypes)
		r.Get("/api/servers/{id}/dayz/mod-loot", s.handleDayzModLoot)
		r.Post("/api/servers/{id}/dayz/import-mod-types", s.handleDayzImportModTypes)
		r.Post("/api/servers/{id}/dayz/reset", s.handleDayzResetNorn)
		r.Post("/api/servers/{id}/rcon", s.handleServerRcon)
		r.Get("/api/servers/{id}/logs", s.handleServerLogs)     // WebSocket
		r.Get("/api/servers/{id}/console", s.handleConsole)     // WebSocket

		// Files
		r.Get("/api/servers/{id}/files", s.handleListFiles)
		r.Get("/api/servers/{id}/files/content", s.handleReadFile)
		r.Put("/api/servers/{id}/files/content", s.handleWriteFile)
		r.Delete("/api/servers/{id}/files", s.handleDeleteFile)
		r.Post("/api/servers/{id}/files/upload", s.handleUploadFile)
		r.Get("/api/servers/{id}/files/download", s.handleDownloadFile)

		// Backup targets (admin-only global config)
		r.Get("/api/backup/targets", s.requireAdmin(s.handleListBackupTargets))
		r.Post("/api/backup/targets", s.requireAdmin(s.handleCreateBackupTarget))
		r.Delete("/api/backup/targets/{id}", s.requireAdmin(s.handleDeleteBackupTarget))
		r.Post("/api/backup/targets/{id}/test", s.requireAdmin(s.handleTestBackupTarget))

		// Backups (per-server, RBAC: server.backup)
		r.Get("/api/servers/{id}/backups", s.handleListBackups)
		r.Post("/api/servers/{id}/backup", s.handleRunBackup)
		r.Post("/api/backups/{id}/restore", s.handleRestoreBackup)
		r.Delete("/api/backups/{id}", s.handleDeleteBackup)

		// Schedules
		r.Get("/api/schedules", s.handleListSchedules)
		r.Post("/api/schedules", s.handleCreateSchedule)
		r.Put("/api/schedules/{id}", s.handleUpdateSchedule)
		r.Delete("/api/schedules/{id}", s.handleDeleteSchedule)
		r.Post("/api/schedules/{id}/run", s.handleRunSchedule)

		// Steam authorization (admin-only)
		r.Get("/api/steam/account", s.requireAdmin(s.handleGetSteamAccount))
		r.Post("/api/steam/send-code", s.requireAdmin(s.handleSteamSendCode))
		r.Post("/api/steam/authorize", s.requireAdmin(s.handleAuthorizeSteam))
		r.Delete("/api/steam/account", s.requireAdmin(s.handleDeleteSteamAccount))

		// Notification channels (admin-only)
		r.Get("/api/notifications", s.requireAdmin(s.handleListNotifications))
		r.Post("/api/notifications", s.requireAdmin(s.handleCreateNotification))
		r.Delete("/api/notifications/{id}", s.requireAdmin(s.handleDeleteNotification))
		r.Post("/api/notifications/{id}/test", s.requireAdmin(s.handleTestNotification))

		// Centralized ban management (admin-only)
		r.Get("/api/bans", s.requireAdmin(s.handleListBans))
		r.Post("/api/bans", s.requireAdmin(s.handleCreateBan))
		r.Delete("/api/bans/{id}", s.requireAdmin(s.handleDeleteBan))

		// Violation auto-action rules (admin-only)
		r.Get("/api/violations", s.requireAdmin(s.handleListViolations))
		r.Post("/api/violations", s.requireAdmin(s.handleCreateViolation))
		r.Put("/api/violations/{id}", s.requireAdmin(s.handleUpdateViolation))
		r.Delete("/api/violations/{id}", s.requireAdmin(s.handleDeleteViolation))

		// Message templates (admin)
		r.Get("/api/templates", s.handleListTemplates)
		r.Post("/api/templates", s.requireAdmin(s.handleSaveTemplate))
		r.Delete("/api/templates/{id}", s.requireAdmin(s.handleDeleteTemplate))

		// Realms
		r.Get("/api/realms", s.handleListRealms)
		r.Post("/api/realms", s.handleCreateRealm)
		r.Put("/api/realms/{id}", s.handleUpdateRealm)
		r.Delete("/api/realms/{id}", s.handleDeleteRealm)

		// Users (admin only)
		r.Get("/api/users", s.requireAdmin(s.handleListUsers))
		r.Post("/api/users", s.requireAdmin(s.handleCreateUser))
		r.Put("/api/users/{id}", s.requireAdmin(s.handleUpdateUser))
		r.Delete("/api/users/{id}", s.requireAdmin(s.handleDeleteUser))
		r.Get("/api/users/{id}/permissions", s.requireAdmin(s.handleGetUserPermissions))
		r.Put("/api/users/{id}/permissions", s.requireAdmin(s.handleSetUserPermissions))
		r.Get("/api/permissions/catalog", s.requireAdmin(s.handlePermissionsCatalog))

		// Network settings (public hostname / connect address) + UPnP
		r.Get("/api/settings/network", s.handleGetNetworkSettings)
		r.Put("/api/settings/network", s.requireAdmin(s.handleSetNetworkSettings))
		r.Get("/api/upnp/status", s.requireAdmin(s.handleUPnPStatus))
		r.Get("/api/settings/unifi", s.requireAdmin(s.handleGetUnifiSettings))
		r.Put("/api/settings/unifi", s.requireAdmin(s.handleSetUnifiSettings))
		r.Post("/api/settings/unifi/test", s.requireAdmin(s.handleTestUnifi))
		r.Get("/api/settings/npm", s.requireAdmin(s.handleGetNpmSettings))
		r.Put("/api/settings/npm", s.requireAdmin(s.handleSetNpmSettings))
		r.Post("/api/settings/npm/test", s.requireAdmin(s.handleTestNpm))

		// Per-server user delegation (server-centric view of server-scoped grants)
		r.Get("/api/servers/{id}/delegates", s.requireAdmin(s.handleListDelegates))
		r.Put("/api/servers/{id}/delegates", s.requireAdmin(s.handleSetDelegates))

		// Audit log
		r.Get("/api/audit", s.requireAdmin(s.handleAuditLog))

		// System info
		r.Get("/api/system/info", s.requireAdmin(s.handleSystemInfo))
	})

	// Static assets + SPA fallback (serve index.html for client-side routes).
	r.Handle("/*", s.spaHandler())

	return r
}

// spaHandler serves embedded static files, falling back to index.html for any
// path that isn't an existing asset or an /api route — so deep links like
// /servers/abc work with client-side routing.
func (s *Server) spaHandler() http.HandlerFunc {
	fileServer := http.FileServer(http.FS(s.webFS))
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		// Does the requested file exist in the embedded FS?
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := s.webFS.Open(p); err == nil {
			f.Close()
			// Go's FileServer doesn't know .webmanifest; set it for PWA install.
			if strings.HasSuffix(p, ".webmanifest") {
				w.Header().Set("Content-Type", "application/manifest+json")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// Fallback: serve index.html for SPA routes.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	}
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenStr := extractToken(r)
		if tokenStr == "" {
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// API tokens (prefix) authenticate automation as their owning user.
		if strings.HasPrefix(tokenStr, auth.APITokenPrefix) {
			claims := s.claimsForAPIToken(r, tokenStr)
			if claims == nil {
				jsonError(w, "invalid api token", http.StatusUnauthorized)
				return
			}
			r = r.WithContext(withClaims(r.Context(), claims))
			next.ServeHTTP(w, r)
			return
		}
		claims, err := auth.ParseToken(tokenStr, s.cfg.Auth.SecretKey)
		if err != nil {
			jsonError(w, "invalid token", http.StatusUnauthorized)
			return
		}
		r = r.WithContext(withClaims(r.Context(), claims))
		next.ServeHTTP(w, r)
	})
}

// claimsForAPIToken resolves an API token to its owner's claims, or nil.
func (s *Server) claimsForAPIToken(r *http.Request, token string) *auth.Claims {
	hash := auth.HashToken(token)
	var userID, username, role string
	err := s.db.QueryRowContext(r.Context(), `
		SELECT u.id, u.username, u.role FROM api_tokens t
		JOIN users u ON u.id = t.user_id
		WHERE t.token_hash=? AND u.disabled=0`, hash).Scan(&userID, &username, &role)
	if err != nil {
		return nil
	}
	s.db.Exec("UPDATE api_tokens SET last_used_at=datetime('now') WHERE token_hash=?", hash)
	return &auth.Claims{UserID: userID, Username: username, Role: role}
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := claimsFromContext(r.Context())
		if claims == nil || claims.Role != "admin" {
			jsonError(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}
