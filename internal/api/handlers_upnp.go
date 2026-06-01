package api

import (
	"context"
	"net/http"

	"github.com/kristianwind/yggdrasil/internal/upnp"
)

// upnpLease is the mapping lease in seconds. 0 = permanent (removed on stop);
// some routers reject 0, but it's the simplest and we remove mappings on stop.
const upnpLease = 0

func (s *Server) upnpEnabled(ctx context.Context) bool {
	return s.getSetting(ctx, "upnp_enabled") == "1"
}

type portProto struct {
	Port  int
	Proto string
}

// serverPortProtos returns the (host port, protocol) pairs to map for a server.
func (s *Server) serverPortProtos(ctx context.Context, serverID string) []portProto {
	rt, err := s.loadRuntime(ctx, serverID)
	if err != nil {
		return nil
	}
	var out []portProto
	for _, p := range rt.gs.Ports {
		if hp := rt.ports[p.Name]; hp > 0 {
			proto := p.Protocol
			if proto == "" {
				proto = "tcp"
			}
			out = append(out, portProto{Port: hp, Proto: proto})
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
	for _, pp := range s.serverPortProtos(ctx, serverID) {
		_ = cl.AddMapping(pp.Port, pp.Proto, "Yggdrasil: "+serverName, upnpLease)
	}
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
	for _, pp := range s.serverPortProtos(ctx, serverID) {
		_ = cl.DeleteMapping(pp.Port, pp.Proto)
	}
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
