package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
)

// Passkeys (WebAuthn/FIDO2) supplement password+TOTP: a user registers one or
// more passkeys under Settings, then signs in passwordless (the passkey is
// itself multi-factor: device possession + user verification). Password+TOTP
// stay as fallback (e.g. for LAN/HTTP access, where WebAuthn's secure-context
// requirement isn't met).

// webAuthnFor builds a relying party bound to the domain the request arrived on,
// so passkeys work on whatever host the panel is served as (RP ID = hostname,
// origin = scheme://host). Browsers only allow WebAuthn in a secure context, so
// this effectively works over HTTPS (or localhost) — plain-HTTP LAN access
// simply won't offer passkeys and falls back to password+TOTP.
// rpParams derives the WebAuthn Relying Party ID (bare hostname) and the browser
// origin (scheme://host[:port]) from the incoming request, honouring the
// reverse-proxy's X-Forwarded-Proto.
func rpParams(r *http.Request) (rpID, origin string) {
	rpID = r.Host
	if h, _, err := net.SplitHostPort(rpID); err == nil {
		rpID = h
	}
	scheme := "https"
	if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
		scheme = xf
	} else if r.TLS == nil {
		scheme = "http"
	}
	return rpID, scheme + "://" + r.Host
}

func (s *Server) webAuthnFor(r *http.Request) (*webauthn.WebAuthn, error) {
	rpID, origin := rpParams(r)
	return webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: "Yggdrasil",
		RPOrigins:     []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
	})
}

// waUser adapts a Yggdrasil user + its stored passkeys to webauthn.User.
type waUser struct {
	id       string
	username string
	role     string
	tokenVer int
	creds    []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return []byte(u.id) }
func (u *waUser) WebAuthnName() string                       { return u.username }
func (u *waUser) WebAuthnDisplayName() string                { return u.username }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (s *Server) waLoadUser(ctx context.Context, userID string) (*waUser, error) {
	u := &waUser{id: userID}
	if err := s.db.QueryRowContext(ctx,
		"SELECT username, role, COALESCE(token_version,0) FROM users WHERE id=? AND disabled=0",
		userID).Scan(&u.username, &u.role, &u.tokenVer); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT cred_json FROM webauthn_credentials WHERE user_id=?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var cj string
		if rows.Scan(&cj) != nil {
			continue
		}
		var c webauthn.Credential
		if json.Unmarshal([]byte(cj), &c) == nil {
			u.creds = append(u.creds, c)
		}
	}
	return u, nil
}

// --- ceremony session store: holds the challenge between begin and finish ----

type waSessionEntry struct {
	data    *webauthn.SessionData
	expires time.Time
}

type waSessionStore struct {
	mu sync.Mutex
	m  map[string]waSessionEntry
}

var waSessions = &waSessionStore{m: map[string]waSessionEntry{}}

func (st *waSessionStore) put(data *webauthn.SessionData) string {
	id := uuid.NewString()
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	for k, v := range st.m { // opportunistic sweep of expired challenges
		if now.After(v.expires) {
			delete(st.m, k)
		}
	}
	st.m[id] = waSessionEntry{data: data, expires: now.Add(5 * time.Minute)}
	return id
}

func (st *waSessionStore) take(id string) *webauthn.SessionData {
	st.mu.Lock()
	defer st.mu.Unlock()
	v, ok := st.m[id]
	delete(st.m, id)
	if !ok || time.Now().After(v.expires) {
		return nil
	}
	return v.data
}

// --- registration (authenticated) -------------------------------------------

func (s *Server) handleWARegisterBegin(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	if claims == nil {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	wa, err := s.webAuthnFor(r)
	if err != nil {
		jsonError(w, "webauthn unavailable", http.StatusInternalServerError)
		return
	}
	user, err := s.waLoadUser(r.Context(), claims.UserID)
	if err != nil {
		jsonError(w, "user error", http.StatusInternalServerError)
		return
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(user.creds))
	for _, c := range user.creds {
		exclude = append(exclude, c.Descriptor())
	}
	options, session, err := wa.BeginRegistration(user, webauthn.WithExclusions(exclude))
	if err != nil {
		jsonError(w, "begin registration: "+err.Error(), http.StatusBadRequest)
		return
	}
	jsonOK(w, map[string]any{"session": waSessions.put(session), "publicKey": options.Response})
}

func (s *Server) handleWARegisterFinish(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	if claims == nil {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	session := waSessions.take(r.URL.Query().Get("session"))
	if session == nil {
		jsonError(w, "registration session expired — try again", http.StatusBadRequest)
		return
	}
	wa, err := s.webAuthnFor(r)
	if err != nil {
		jsonError(w, "webauthn unavailable", http.StatusInternalServerError)
		return
	}
	user, err := s.waLoadUser(r.Context(), claims.UserID)
	if err != nil {
		jsonError(w, "user error", http.StatusInternalServerError)
		return
	}
	cred, err := wa.FinishRegistration(user, *session, r)
	if err != nil {
		jsonError(w, "registration failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "passkey"
	}
	cj, _ := json.Marshal(cred)
	if _, err := s.db.ExecContext(r.Context(),
		"INSERT INTO webauthn_credentials (id, user_id, cred_id, cred_json, name) VALUES (?,?,?,?,?)",
		uuid.NewString(), claims.UserID, cred.ID, string(cj), name); err != nil {
		jsonError(w, "could not store passkey", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "webauthn.register", "user:"+claims.UserID, map[string]string{"name": name})
	jsonOK(w, map[string]string{"status": "registered", "name": name})
}

func (s *Server) handleWAList(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	if claims == nil {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, name, created_at, COALESCE(last_used,'') FROM webauthn_credentials WHERE user_id=? ORDER BY created_at", claims.UserID)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var id, name, created, lastUsed string
		if rows.Scan(&id, &name, &created, &lastUsed) == nil {
			out = append(out, map[string]string{"id": id, "name": name, "created_at": created, "last_used": lastUsed})
		}
	}
	jsonOK(w, out)
}

func (s *Server) handleWADelete(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	if claims == nil {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := chi.URLParam(r, "id")
	s.db.ExecContext(r.Context(), "DELETE FROM webauthn_credentials WHERE id=? AND user_id=?", id, claims.UserID)
	s.auditLog(r, "webauthn.delete", "user:"+claims.UserID, map[string]string{"cred": id})
	jsonOK(w, map[string]string{"status": "deleted"})
}

// --- passwordless login (public) --------------------------------------------

func (s *Server) handleWALoginBegin(w http.ResponseWriter, r *http.Request) {
	if !loginLimiter.allow(r.RemoteAddr) {
		jsonError(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}
	wa, err := s.webAuthnFor(r)
	if err != nil {
		jsonError(w, "webauthn unavailable", http.StatusInternalServerError)
		return
	}
	options, session, err := wa.BeginDiscoverableLogin()
	if err != nil {
		jsonError(w, "begin login: "+err.Error(), http.StatusBadRequest)
		return
	}
	jsonOK(w, map[string]any{"session": waSessions.put(session), "publicKey": options.Response})
}

func (s *Server) handleWALoginFinish(w http.ResponseWriter, r *http.Request) {
	if !loginLimiter.allow(r.RemoteAddr) {
		jsonError(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}
	session := waSessions.take(r.URL.Query().Get("session"))
	if session == nil {
		jsonError(w, "login session expired — try again", http.StatusBadRequest)
		return
	}
	wa, err := s.webAuthnFor(r)
	if err != nil {
		jsonError(w, "webauthn unavailable", http.StatusInternalServerError)
		return
	}
	var resolved *waUser
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		u, err := s.waLoadUser(r.Context(), string(userHandle))
		if err != nil {
			return nil, err
		}
		resolved = u
		return u, nil
	}
	cred, err := wa.FinishDiscoverableLogin(handler, *session, r)
	if err != nil || resolved == nil {
		jsonError(w, "passkey login failed", http.StatusUnauthorized)
		return
	}
	// Persist the updated credential (sign count) + last-used timestamp.
	cj, _ := json.Marshal(cred)
	s.db.ExecContext(r.Context(),
		"UPDATE webauthn_credentials SET cred_json=?, last_used=datetime('now') WHERE cred_id=?", string(cj), cred.ID)
	s.auditLog(r, "webauthn.login", "user:"+resolved.id, nil)
	s.issueSession(w, resolved.id, resolved.username, resolved.role, resolved.tokenVer)
}
