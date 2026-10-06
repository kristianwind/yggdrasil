package api

import (
	"context"
	"net/http"
	"strings"

	"fmt"
	"github.com/kristianwind/yggdrasil/internal/upnp"
	"log"
)

// upnpLease is the mapping lease in seconds. 0 = permanent (removed on stop);
// some routers reject 0, but it's the simplest and we remove mappings on stop.
const upnpLease = 0

func (s *Server) upnpEnabled(ctx context.Context) bool {
	return s.getSetting(ctx, "upnp_enabled") == "1"
}

// upnpProtos expands a rune protocol into the protocols UPnP must be called
// with: AddPortMapping/DeletePortMapping each take one NewProtocol, so "tcp+udp"
// is two calls. Add and remove both go through here, so a pair that was opened
// as two mappings is closed as two.
func upnpProtos(protocol string) []string {
	if protocol == "tcp+udp" {
		return []string{"tcp", "udp"}
	}
	if protocol == "udp" {
		return []string{"udp"}
	}
	return []string{"tcp"}
}

type portProto struct {
	Port  int
	Proto string
	Admin bool // admin/RCON port — reachable locally but never WAN-forwarded
}

// serverPortProtos returns the (host port, protocol) pairs to map for a server.
// Admin/RCON ports are flagged so the WAN-forwarding paths can skip them: an
// RCON console must never be opened to the internet by auto-forward.
func (s *Server) serverPortProtos(ctx context.Context, serverID string) []portProto {
	rt, err := s.loadRuntime(ctx, serverID)
	if err != nil {
		return nil
	}
	var out []portProto
	for _, p := range rt.gs.Ports {
		if hp := rt.ports[p.Name]; hp > 0 {
			// The rune's protocol verbatim, "tcp+udp" included. Each consumer
			// wants it differently: UPnP needs one call per protocol, UniFi has a
			// single tcp_udp rule for the pair. Splitting here would force UniFi
			// to create two identically-named rules for one port — the shape of
			// duplicate that makes a router's forward list unreadable.
			out = append(out, portProto{Port: hp, Proto: p.Protocol, Admin: strings.EqualFold(p.Name, "rcon")})
		}
	}
	return out
}

// upnpAddServer opens router port mappings for a server (best-effort, async).
func (s *Server) upnpAddServer(serverID, serverName string) {
	defer recoverLog("upnpAddServer")
	ctx := context.Background()
	if !s.upnpEnabled(ctx) {
		return
	}
	cl, err := upnp.Discover()
	if err != nil {
		return // no IGD; manual forwarding helper is shown instead
	}
	var opened, failed []string
	for _, pp := range s.serverPortProtos(ctx, serverID) {
		if pp.Admin {
			continue // never WAN-forward the RCON/admin port
		}
		for _, proto := range upnpProtos(pp.Proto) {
			if err := cl.AddMapping(pp.Port, proto, "Yggdrasil: "+serverName, upnpLease); err != nil {
				failed = append(failed, fmt.Sprintf("%d/%s (%v)", pp.Port, proto, err))
				continue
			}
			opened = append(opened, fmt.Sprintf("%d/%s", pp.Port, proto))
		}
	}
	// Opening a port to the internet is the most consequential thing this panel
	// does on an operator's behalf, and it used to leave no trace: the result was
	// discarded, nothing was logged, and no audit entry was written. So the
	// question "is this port open, and who caused it" had no answer inside the
	// panel -- which is also the input a teardown needs.
	s.logUPnP(serverID, serverName, "opened", opened, failed)
}

// logUPnP records what the router was asked for and what it answered.
func (s *Server) logUPnP(serverID, serverName, verb string, done, failed []string) {
	if len(done) == 0 && len(failed) == 0 {
		return
	}
	if len(done) > 0 {
		log.Printf("upnp: %s %s on the router for %s", verb, strings.Join(done, ", "), serverName)
	}
	for _, f := range failed {
		log.Printf("upnp: could NOT %s %s for %s", strings.TrimSuffix(verb, "ed"), f, serverName)
	}
	s.auditSystem("upnp."+verb, "server:"+serverID, "yggdrasil", map[string]any{
		"server": serverName,
		"ports":  done,
		"failed": failed,
	})
}

// upnpRemoveServer removes a server's router port mappings (best-effort, async).
func (s *Server) upnpRemoveServer(serverID string) {
	defer recoverLog("upnpRemoveServer")
	ctx := context.Background()
	if !s.upnpEnabled(ctx) {
		return
	}
	cl, err := upnp.Discover()
	if err != nil {
		return
	}
	var closed, failed []string
	for _, pp := range s.serverPortProtos(ctx, serverID) {
		for _, proto := range upnpProtos(pp.Proto) {
			if err := cl.DeleteMapping(pp.Port, proto); err != nil {
				failed = append(failed, fmt.Sprintf("%d/%s (%v)", pp.Port, proto, err))
				continue
			}
			closed = append(closed, fmt.Sprintf("%d/%s", pp.Port, proto))
		}
	}
	// A failure here is the one that matters most: the mapping stays open and the
	// panel goes on believing it closed it.
	s.logUPnP(serverID, s.serverName(serverID), "closed", closed, failed)
}

// handleUPnPStatus reports whether UPnP is enabled and whether a gateway is
// reachable (for the Settings UI). Discovery is quick and read-only.
func (s *Server) handleUPnPStatus(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"enabled": s.upnpEnabled(r.Context())}
	cl, err := upnp.Discover()
	if err != nil {
		resp["gateway"] = false
		resp["message"] = err.Error()
		jsonOK(w, resp)
		return
	}
	resp["gateway"] = true
	resp["local_ip"] = cl.LocalIP()
	if ip, e := cl.ExternalIP(); e == nil {
		resp["external_ip"] = ip
	}
	jsonOK(w, resp)
}
