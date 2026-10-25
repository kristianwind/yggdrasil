package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/registry"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Logins for container registries.
//
// The panel used to pull every image anonymously, so a rune could only ever name
// a public image. That rules out the obvious way to run your own private app: a
// private repository on your own Gitea, whose package registry follows the
// repository owner's visibility. A login stored here is used for exactly the
// images that name its registry, and for nothing else:
//
//   - It is matched by the registry an image reference names, after Docker's own
//     normalisation — "nginx" is docker.io, "gitea.example.com/kw/app" is
//     gitea.example.com — so a login for one registry is never offered to another.
//   - Docker receives it per pull, in the request. Nothing is written to the
//     daemon's or the host's credential store.
//   - The secret is encrypted at rest and never returned by the API.

// dockerHubAliases are the names Docker Hub goes by. All of them mean docker.io,
// which is what an image reference with no registry normalises to.
var dockerHubAliases = map[string]bool{
	"docker.io": true, "index.docker.io": true, "registry-1.docker.io": true,
	"hub.docker.com": true, "registry.hub.docker.com": true,
}

var registryHostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)

// normalizeRegistry turns what an admin typed (a URL, a host, "Docker Hub's"
// many names) into the host[:port] an image reference resolves to.
func normalizeRegistry(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return "", errors.New("registry is required, for example gitea.example.com")
	}
	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil || u.Host == "" {
			return "", errors.New("registry must be a host such as gitea.example.com")
		}
		v = u.Host
	}
	v = strings.TrimRight(v, "/")
	if i := strings.Index(v, "/"); i >= 0 {
		v = v[:i] // a pasted image path: keep the registry part
	}
	if dockerHubAliases[v] {
		return "docker.io", nil
	}
	if !registryHostRe.MatchString(v) {
		return "", errors.New("registry must be a host such as gitea.example.com or ghcr.io")
	}
	return v, nil
}

// registryOf names the registry an image reference pulls from, the way Docker
// resolves it: no registry part means docker.io. "" when the reference does not
// parse — typically a rune placeholder left unfilled — so no login is offered.
func registryOf(ref string) string {
	named, err := reference.ParseNormalizedNamed(strings.TrimSpace(ref))
	if err != nil {
		return ""
	}
	d := strings.ToLower(reference.Domain(named))
	if dockerHubAliases[d] {
		return "docker.io"
	}
	return d
}

// registryAuthFor is the encoded login Docker should present when pulling ref,
// or "" for an anonymous pull.
func (s *Server) registryAuthFor(ctx context.Context, ref string) string {
	reg := registryOf(ref)
	if reg == "" || s.cipher == nil {
		return ""
	}
	var user, enc string
	if err := s.db.QueryRowContext(ctx,
		"SELECT username, secret_enc FROM registry_logins WHERE registry=?", reg).Scan(&user, &enc); err != nil || enc == "" {
		return ""
	}
	secret, err := s.cipher.Decrypt(enc)
	if err != nil {
		return ""
	}
	auth, err := registry.EncodeAuthConfig(registry.AuthConfig{Username: user, Password: secret, ServerAddress: reg})
	if err != nil {
		return ""
	}
	return auth
}

// pullImage pulls ref with the login for its registry, if one is stored. Every
// pull the panel makes for a server goes through here.
func (s *Server) pullImage(ctx context.Context, ref string, out io.Writer) error {
	return s.docker.PullImageAuth(ctx, ref, s.registryAuthFor(ctx, ref), out)
}

type registryLoginDTO struct {
	ID        string `json:"id"`
	Registry  string `json:"registry"`
	Username  string `json:"username"`
	Secret    string `json:"secret,omitempty"` // write-only
	HasSecret bool   `json:"has_secret"`
	CreatedAt string `json:"created_at,omitempty"`
}

func (s *Server) handleListRegistryLogins(w http.ResponseWriter, r *http.Request) {
	out := []registryLoginDTO{}
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, registry, username, secret_enc<>'', created_at FROM registry_logins ORDER BY registry")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var d registryLoginDTO
		if rows.Scan(&d.ID, &d.Registry, &d.Username, &d.HasSecret, &d.CreatedAt) == nil {
			out = append(out, d)
		}
	}
	jsonOK(w, out)
}

// handleSaveRegistryLogin adds a login, or replaces the one for that registry.
func (s *Server) handleSaveRegistryLogin(w http.ResponseWriter, r *http.Request) {
	var d registryLoginDTO
	if decodeJSON(r, &d) != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}
	reg, err := normalizeRegistry(d.Registry)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	d.Username = strings.TrimSpace(d.Username)
	d.Secret = strings.TrimSpace(d.Secret)
	if d.Username == "" || d.Secret == "" {
		jsonError(w, "username and password or access token are required", http.StatusBadRequest)
		return
	}
	if s.cipher == nil {
		jsonError(w, "no encryption key available to store the login", http.StatusInternalServerError)
		return
	}
	enc, err := s.cipher.Encrypt(d.Secret)
	if err != nil {
		jsonError(w, "could not encrypt the login", http.StatusInternalServerError)
		return
	}
	_, err = s.db.ExecContext(r.Context(), `
		INSERT INTO registry_logins (id, registry, username, secret_enc) VALUES (?,?,?,?)
		ON CONFLICT(registry) DO UPDATE SET username=excluded.username, secret_enc=excluded.secret_enc`,
		uuid.New().String(), reg, d.Username, enc)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "registry_login.save", "registry:"+reg, map[string]string{"username": d.Username})
	jsonOK(w, registryLoginDTO{Registry: reg, Username: d.Username, HasSecret: true})
}

func (s *Server) handleDeleteRegistryLogin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var reg string
	s.db.QueryRowContext(r.Context(), "SELECT registry FROM registry_logins WHERE id=?", id).Scan(&reg) //nolint:errcheck
	if reg == "" {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	s.db.ExecContext(r.Context(), "DELETE FROM registry_logins WHERE id=?", id)
	s.auditLog(r, "registry_login.delete", "registry:"+reg, nil)
	jsonOK(w, map[string]string{"status": "deleted"})
}

// handleTestRegistryLogin logs in to the registry with the stored login, so a
// wrong token is found here rather than as a server that will not start.
func (s *Server) handleTestRegistryLogin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var reg, user, enc string
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT registry, username, secret_enc FROM registry_logins WHERE id=?", id).Scan(&reg, &user, &enc); err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	secret, err := s.cipher.Decrypt(enc)
	if err != nil {
		jsonError(w, "could not decrypt the stored login", http.StatusInternalServerError)
		return
	}
	if s.docker == nil {
		jsonError(w, "docker is not available", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	status, err := s.docker.RegistryLogin(ctx, reg, user, secret)
	if err != nil {
		// 400, not 502: the reason has to survive any proxy in front of the panel.
		jsonError(w, reg+" refused the login: "+err.Error(), http.StatusBadRequest)
		return
	}
	jsonOK(w, map[string]any{"ok": true, "registry": reg, "status": status})
}
