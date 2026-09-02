package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// A server's public hostnames.
//
// There used to be exactly one, in servers.subdomain, pointed at whichever TCP
// port the rune happened to call "web". That answers the common case and nothing
// else: an app with a separate admin UI cannot expose it, and a server cannot
// answer on two domains — which is the whole point of a tunnel that can serve
// several zones.
//
// The primary route is still servers.subdomain, deliberately. Migrating it into
// this table would have been tidier and would have moved every existing install's
// working configuration to prove a point. Extra routes live in server_routes;
// both kinds flow through the same provisioning path, so every lifecycle hook
// that already called cfAddServer/npmAddServer covers them without being touched.

// serverRoute is one hostname a server answers on.
type serverRoute struct {
	// ID is empty for the primary route, which lives on the servers row rather
	// than in server_routes. Callers use it to decide where to record state.
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	// PortName is a port the rune declares. Empty means the automatic choice.
	PortName string `json:"port_name"`
	Primary  bool   `json:"primary"`
}

// serverRoutes returns every hostname configured for a server, primary first.
func (s *Server) serverRoutes(ctx context.Context, serverID string) []serverRoute {
	out := []serverRoute{}
	var sub, subPort string
	s.db.QueryRowContext(ctx,
		"SELECT COALESCE(subdomain,''), COALESCE(subdomain_port,'') FROM servers WHERE id=?",
		serverID).Scan(&sub, &subPort)
	if normalizeSubdomain(sub) != "" {
		out = append(out, serverRoute{Hostname: sub, PortName: subPort, Primary: true})
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, hostname, COALESCE(port_name,'') FROM server_routes WHERE server_id=? ORDER BY hostname",
		serverID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var r serverRoute
		if rows.Scan(&r.ID, &r.Hostname, &r.PortName) == nil && normalizeSubdomain(r.Hostname) != "" {
			out = append(out, r)
		}
	}
	return out
}

// serverRoutePort resolves the host port a route should reach.
//
// An empty port name keeps the original behaviour exactly — serverWebPort, which
// prefers the port named "web" and falls back to the first TCP one. A named port
// must be declared by the rune and must be TCP: a hostname pointed at a UDP game
// port would produce a proxy rule that can never answer, and failing here is much
// easier to understand than a route that exists and times out.
func (s *Server) serverRoutePort(ctx context.Context, serverID, portName string) int {
	if strings.TrimSpace(portName) == "" {
		return s.serverWebPort(ctx, serverID)
	}
	rt, err := s.loadRuntime(ctx, serverID)
	if err != nil {
		return 0
	}
	for _, p := range rt.gs.Ports {
		if p.Name != portName {
			continue
		}
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		if proto != "tcp" {
			return 0
		}
		return rt.ports[p.Name]
	}
	return 0
}

// routableS is the shape the editor needs: which ports a server can actually be
// routed at, so the UI offers a list rather than a free-text field somebody has
// to guess into.
type routablePort struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

func (s *Server) routablePorts(ctx context.Context, serverID string) []routablePort {
	out := []routablePort{}
	rt, err := s.loadRuntime(ctx, serverID)
	if err != nil {
		return out
	}
	for _, p := range rt.gs.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		if proto != "tcp" {
			continue
		}
		if hp := rt.ports[p.Name]; hp > 0 {
			out = append(out, routablePort{Name: p.Name, Port: hp})
		}
	}
	return out
}

// --- HTTP ---

func (s *Server) handleListServerRoutes(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerView, s.serverTarget(r.Context(), id)) {
		return
	}
	jsonOK(w, map[string]any{
		"routes": s.serverRoutes(r.Context(), id),
		"ports":  s.routablePorts(r.Context(), id),
	})
}

// handleAddServerRoute adds an extra hostname. Admin-only, like the ports editor:
// a hostname is a public name for somebody else's machine to resolve, and the
// blast radius of getting it wrong is not confined to one server.
func (s *Server) handleAddServerRoute(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Hostname string `json:"hostname"`
		PortName string `json:"port_name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	host := normalizeSubdomain(req.Hostname)
	if host == "" {
		jsonError(w, "hostname required", http.StatusBadRequest)
		return
	}
	// The same rule the primary follows, applied where it can still be explained:
	// a bare label becomes a subdomain of the configured base domain, and without
	// one there is nothing to append it to.
	if !strings.Contains(host, ".") &&
		s.getSetting(r.Context(), "cf_base_domain") == "" &&
		s.getSetting(r.Context(), "npm_base_domain") == "" {
		jsonError(w, "no base domain is configured, so a bare name has nothing to attach to — "+
			"enter a full hostname, or set a base domain under Settings → Network",
			http.StatusBadRequest)
		return
	}
	if req.PortName != "" && s.serverRoutePort(r.Context(), id, req.PortName) == 0 {
		jsonError(w, "that port is not a TCP port this server declares", http.StatusBadRequest)
		return
	}
	// One hostname, one destination. Two routes claiming the same name would fight
	// on every start, and the loser would be whichever ran last.
	var clash int
	s.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM server_routes WHERE LOWER(hostname)=?", host).Scan(&clash)
	var subClash int
	s.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM servers WHERE LOWER(COALESCE(subdomain,''))=?", host).Scan(&subClash)
	if clash+subClash > 0 {
		jsonError(w, "that hostname is already used by a server on this panel", http.StatusConflict)
		return
	}

	rid := uuid.New().String()
	if _, err := s.db.ExecContext(r.Context(),
		"INSERT INTO server_routes (id, server_id, hostname, port_name) VALUES (?,?,?,?)",
		rid, id, host, req.PortName); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "server.route.add", id, map[string]any{"hostname": host, "port_name": req.PortName})

	// Provision immediately if the server is up, so the hostname works without a
	// restart. On a stopped server it is created on next start, like the primary.
	go s.applyServerRoutes(id)
	jsonOK(w, map[string]any{"id": rid, "hostname": host, "port_name": req.PortName})
}

func (s *Server) handleDeleteServerRoute(w http.ResponseWriter, r *http.Request) {
	id, rid := chi.URLParam(r, "id"), chi.URLParam(r, "routeID")
	var host string
	s.db.QueryRowContext(r.Context(),
		"SELECT COALESCE(cf_hostname,'') FROM server_routes WHERE id=? AND server_id=?",
		rid, id).Scan(&host)

	// Tear the live route down BEFORE forgetting it. The other order loses the
	// only record of what was provisioned and leaves an ingress rule and a DNS
	// record pointing at a server that no longer claims them.
	s.dropRouteHost(r.Context(), rid)

	if _, err := s.db.ExecContext(r.Context(),
		"DELETE FROM server_routes WHERE id=? AND server_id=?", rid, id); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.auditLog(r, "server.route.remove", id, map[string]any{"hostname": host})
	jsonOK(w, map[string]string{"status": "removed"})
}

// applyServerRoutes re-runs provisioning for a server (both providers).
func (s *Server) applyServerRoutes(serverID string) {
	defer recoverLog("applyServerRoutes")
	var name, status string
	s.db.QueryRowContext(context.Background(),
		"SELECT name, status FROM servers WHERE id=?", serverID).Scan(&name, &status)
	if status != "running" {
		return // nothing to point at yet; start will do it
	}
	s.npmAddServer(serverID, name)
	s.cfAddServer(serverID, name)
}
