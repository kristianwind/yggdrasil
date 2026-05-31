package api

import (
	"database/sql"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/kristianwind/yggdrasil/internal/auth"
	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/docker"
)

type Server struct {
	cfg     *config.Config
	db      *sql.DB
	docker  *docker.Client
	router  *chi.Mux
	webFS   fs.FS
	install *progressHub // live install/build output, keyed by server id
}

func New(cfg *config.Config, db *sql.DB, dc *docker.Client, webFS embed.FS) *Server {
	subFS, _ := fs.Sub(webFS, "web/dist")

	s := &Server{
		cfg:     cfg,
		db:      db,
		docker:  dc,
		webFS:   subFS,
		install: newProgressHub(),
	}
	s.router = s.buildRouter()
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
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		AllowCredentials: true,
	}))
	r.Use(secureHeaders)

	// Public routes
	r.Post("/api/auth/login", s.handleLogin)

	// Authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware)
		r.Post("/api/auth/logout", s.handleLogout)
		r.Get("/api/auth/me", s.handleMe)

		// Gameskills (Runes)
		r.Get("/api/gameskills", s.handleListGameskills)
		r.Post("/api/gameskills", s.handleUploadGameskill)
		r.Get("/api/gameskills/{id}", s.handleGetGameskill)
		r.Delete("/api/gameskills/{id}", s.handleDeleteGameskill)

		// Servers
		r.Get("/api/servers", s.handleListServers)
		r.Post("/api/servers", s.handleCreateServer)
		r.Get("/api/servers/{id}", s.handleGetServer)
		r.Delete("/api/servers/{id}", s.handleDeleteServer)
		r.Post("/api/servers/{id}/install", s.handleInstallServer)
		r.Get("/api/servers/{id}/install/log", s.handleInstallLog) // WebSocket
		r.Post("/api/servers/{id}/start", s.handleStartServer)
		r.Post("/api/servers/{id}/stop", s.handleStopServer)
		r.Post("/api/servers/{id}/restart", s.handleRestartServer)
		r.Get("/api/servers/{id}/stats", s.handleServerStats)
		r.Get("/api/servers/{id}/query", s.handleServerQuery)
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
		claims, err := auth.ParseToken(tokenStr, s.cfg.Auth.SecretKey)
		if err != nil {
			jsonError(w, "invalid token", http.StatusUnauthorized)
			return
		}
		r = r.WithContext(withClaims(r.Context(), claims))
		next.ServeHTTP(w, r)
	})
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
