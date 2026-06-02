package api

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/query"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// Reachability answers "is this server actually reachable from the internet?" by
// probing its public connect address (public hostname / detected external IP) —
// not 127.0.0.1. A success proves both that the server is up AND that the port is
// forwarded. NOTE: this works from the panel host only when the router supports
// NAT loopback (hairpin); without it the probe can read offline even when the
// server is reachable from outside.

type reachEntry struct {
	at   time.Time
	data map[string]any
}

var (
	reachMu    sync.Mutex
	reachStore = map[string]reachEntry{}
)

func (s *Server) handleServerReachability(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerView, s.serverTarget(r.Context(), id)) {
		return
	}
	jsonOK(w, s.checkReachable(r.Context(), id))
}

func (s *Server) checkReachable(ctx context.Context, id string) map[string]any {
	reachMu.Lock()
	if e, ok := reachStore[id]; ok && time.Since(e.at) < 30*time.Second {
		reachMu.Unlock()
		return e.data
	}
	reachMu.Unlock()

	host := firstNonEmpty(s.getSetting(ctx, "public_hostname"), s.detectPublicAddr())
	out := map[string]any{"reachable": false, "host": host}
	rt, err := s.loadRuntime(ctx, id)
	if err != nil || host == "" {
		return out
	}

	// Prefer a real protocol probe on the query port (A2S / Minecraft SLP /
	// Bedrock ping); fall back to a plain TCP connect on the game port for games
	// without a query protocol (Terraria, databases).
	port := rt.queryPort()
	if rt.gs.Query != nil && port > 0 {
		out["port"] = port
		if _, err := query.Query(rt.gs.Query.Type, host, port, 4*time.Second); err == nil {
			out["reachable"] = true
		}
	} else {
		gp := rt.ports["game"]
		if gp == 0 {
			gp = port
		}
		out["port"] = gp
		if gp > 0 {
			if c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(gp)), 4*time.Second); err == nil {
				c.Close()
				out["reachable"] = true
			}
		}
	}

	reachMu.Lock()
	reachStore[id] = reachEntry{at: time.Now(), data: out}
	reachMu.Unlock()
	return out
}
