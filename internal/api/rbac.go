package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// loadGrants reads a user's permission grants from the permissions table.
// perms are stored comma-separated in the `perms` column.
func (s *Server) loadGrants(ctx context.Context, userID string) []rbac.Grant {
	rows, err := s.db.QueryContext(ctx,
		"SELECT scope_type, COALESCE(scope_id,''), perms FROM permissions WHERE user_id=?", userID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var grants []rbac.Grant
	for rows.Next() {
		var scopeType, scopeID, perms string
		if err := rows.Scan(&scopeType, &scopeID, &perms); err != nil {
			continue
		}
		g := rbac.Grant{ScopeType: rbac.ScopeType(scopeType), ScopeID: scopeID}
		for _, p := range strings.Split(perms, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				g.Perms = append(g.Perms, rbac.Permission(p))
			}
		}
		grants = append(grants, g)
	}
	return grants
}

// isAdmin reports whether the request is from a global admin.
func isAdmin(r *http.Request) bool {
	c := claimsFromContext(r.Context())
	return c != nil && c.Role == "admin"
}

// can checks a permission against a target. Global admins always pass. On
// denial it writes a 403 and returns false, so handlers can `if !s.can(...) { return }`.
func (s *Server) can(w http.ResponseWriter, r *http.Request, perm rbac.Permission, target rbac.Target) bool {
	if isAdmin(r) {
		return true
	}
	c := claimsFromContext(r.Context())
	if c == nil {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if rbac.Allowed(s.loadGrants(r.Context(), c.UserID), perm, target) {
		return true
	}
	jsonError(w, "forbidden: insufficient permissions", http.StatusForbidden)
	return false
}

// allowed is the non-writing variant, for filtering lists.
func (s *Server) allowed(r *http.Request, perm rbac.Permission, target rbac.Target) bool {
	if isAdmin(r) {
		return true
	}
	c := claimsFromContext(r.Context())
	if c == nil {
		return false
	}
	return rbac.Allowed(s.loadGrants(r.Context(), c.UserID), perm, target)
}

// allPermStrings is every assignable permission as strings — what an admin
// effectively holds on any server.
var allPermStrings = func() []string {
	out := make([]string, len(rbac.All))
	for i, p := range rbac.All {
		out[i] = string(p)
	}
	return out
}()

// permStrings converts a permission slice to strings for the API.
func permStrings(perms []rbac.Permission) []string {
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = string(p)
	}
	return out
}

// serverTarget resolves a server's realm and gameskill into an rbac.Target.
func (s *Server) serverTarget(ctx context.Context, serverID string) rbac.Target {
	var realmID, gsID string
	s.db.QueryRowContext(ctx,
		"SELECT COALESCE(realm_id,''), gameskill_id FROM servers WHERE id=?", serverID).
		Scan(&realmID, &gsID)
	return rbac.Target{ServerID: serverID, RealmID: realmID, GameskillID: gsID}
}

// visibleServerIDs is the set of servers the caller may see, for the handlers
// that answer about the fleet rather than about one server. Admins get (nil,
// true) — every server, nothing to filter.
//
// The fleet endpoints used to query `servers` directly, so a signed-in user
// with a single server-scoped grant received every server's name, its player
// count and the names of the players on it. No privilege escalation, but on a
// panel where servers are delegated to different households that is other
// people's children's usernames.
//
// Grants are loaded BEFORE the server query on purpose: modernc SQLite serves
// database/sql from one connection, so running loadGrants with a result set
// still open deadlocks (it bit handleListServers, and only for non-admins —
// admins skip the lookup, so the happy path hid it).
func (s *Server) visibleServerIDs(r *http.Request) (map[string]bool, bool) {
	if isAdmin(r) {
		return nil, true
	}
	var grants []rbac.Grant
	if c := claimsFromContext(r.Context()); c != nil {
		grants = s.loadGrants(r.Context(), c.UserID)
	}
	rows, err := s.db.QueryContext(r.Context(),
		"SELECT id, COALESCE(realm_id,''), gameskill_id FROM servers")
	if err != nil {
		return map[string]bool{}, false // fail closed
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id, realm, gs string
		if rows.Scan(&id, &realm, &gs) != nil {
			continue
		}
		if rbac.VisibleServer(grants, rbac.Target{ServerID: id, RealmID: realm, GameskillID: gs}) {
			out[id] = true
		}
	}
	return out, false
}
