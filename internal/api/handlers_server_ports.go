package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// validatePortChoice reports whether an explicitly-requested host port may be
// claimed: it must be a valid TCP port, not already picked earlier in this
// request (`taken`), not recorded in port_allocations, and bindable on the host
// right now. An explicit choice is honoured even outside the auto-allocation
// range — keeping a specific port (e.g. a migrated site's :25002) is the whole
// point of letting the user pick one, mirroring pickTransferPorts.
func (s *Server) validatePortChoice(ctx context.Context, port int, taken map[int]bool) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("must be between 1 and 65535")
	}
	if taken[port] {
		return fmt.Errorf("already chosen for another port on this server")
	}
	if !s.portAvailable(ctx, port) {
		return fmt.Errorf("already in use")
	}
	return nil
}

// handleUpdateServerPorts changes one or more of a server's host ports after
// creation. Admin-only. The body is {"ports": {"<name>": <newPort>}}; only the
// listed ports change, each must already exist on the server, and each new port
// must be free. If the server has a live container it is recreated so it rebinds
// to the new port(s) (and its NPM proxy target refreshed); a stopped server just
// records the change and binds it on next start. This is the deferred
// "edit a server's host port after creation" from the v0.2.176 migration work.
func (s *Server) handleUpdateServerPorts(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.getServer(r.Context(), id)
	if err != nil {
		jsonError(w, "server not found", http.StatusNotFound)
		return
	}
	var req struct {
		Ports map[string]int `json:"ports"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if len(req.Ports) == 0 {
		jsonError(w, "no ports given", http.StatusBadRequest)
		return
	}

	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "load runtime: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// protocol per rune port name, for the port_allocations row.
	proto := map[string]string{}
	for _, p := range rt.gs.Ports {
		pr := p.Protocol
		if pr == "" {
			pr = "tcp"
		}
		proto[p.Name] = pr
	}

	newPorts := map[string]int{}
	for name, hp := range rt.ports {
		newPorts[name] = hp
	}
	picked := map[int]bool{} // new ports claimed within this request
	changed := map[string]int{}
	for name, np := range req.Ports {
		cur, ok := rt.ports[name]
		if !ok {
			jsonError(w, fmt.Sprintf("unknown port %q", name), http.StatusBadRequest)
			return
		}
		if np == cur {
			continue // no-op — leave it (and skip the self-bound live check)
		}
		if err := s.validatePortChoice(r.Context(), np, picked); err != nil {
			jsonError(w, fmt.Sprintf("port %d for %q: %s", np, name, err), http.StatusBadRequest)
			return
		}
		picked[np] = true
		newPorts[name] = np
		changed[name] = np
	}
	if len(changed) == 0 {
		jsonOK(w, map[string]any{"ports": newPorts, "changed": false})
		return
	}

	// Persist the new allocations (delete old row by name, insert new) and the
	// ports_json map that recreateAndStart / start read from.
	for name, np := range changed {
		s.db.ExecContext(r.Context(), "DELETE FROM port_allocations WHERE server_id=? AND name=?", id, name)
		s.db.ExecContext(r.Context(),
			"INSERT INTO port_allocations (port, server_id, protocol, name) VALUES (?,?,?,?)",
			np, id, proto[name], name)
	}
	portsJSON, _ := json.Marshal(newPorts)
	s.db.ExecContext(r.Context(), "UPDATE servers SET ports_json=? WHERE id=?", string(portsJSON), id)

	// A running container must be recreated to rebind. A stopped server binds the
	// new ports on its next start, so we leave it stopped.
	recreated := false
	if srv.ContainerID != "" {
		if err := s.recreateAndStart(r.Context(), id); err != nil {
			jsonError(w, "recreate on new port failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		recreated = true
		go s.npmAddServer(id, srv.Name) // refresh the proxy target if it has a subdomain
	}

	s.auditLog(r, "server.ports", "server:"+id, map[string]string{"changed": fmt.Sprintf("%v", changed)})
	jsonOK(w, map[string]any{"ports": newPorts, "changed": true, "recreated": recreated})
}
