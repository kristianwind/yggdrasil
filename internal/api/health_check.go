package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// An HTTP health check, for the gap between "the port is bound" and "the app is
// alive".
//
// Readiness dials the server's web port, and that is the right signal for
// starting up. It is the wrong signal for staying up: Docker's userland proxy
// binds a published port for as long as the container exists, whether or not
// anything inside is listening. So a container that is up with a dead app
// answers a TCP connect exactly like a healthy one, and the panel says running.
//
// Measured on this fleet. The nightly fleet update recreated a VPN-gated
// torrent client; its provider's endpoint was down, the container's killswitch
// kept the client from starting, and the panel showed "running" for an hour
// with a bound port and nothing behind it. An external HTTP monitor caught it
// in seconds, because it asked for a response instead of a connection.
//
// Off unless a path is set. Plenty of servers have no HTTP endpoint worth
// asking, and a check that guesses would page people about game servers that
// were never going to answer.

const (
	healthCheckInterval  = 60 * time.Second
	healthRequestTimeout = 8 * time.Second
	// Three consecutive failures before saying anything. One is a restart, a
	// redeploy, or a slow moment; three across three minutes is a server that is
	// not coming back on its own.
	healthFailuresBeforeAlert = 3
)

// healthState is the per-server strike count and last reported verdict. In
// memory on purpose: a panel restart should re-observe rather than trust what it
// believed before it went down.
type healthState struct {
	mu      sync.Mutex
	strikes map[string]int
	down    map[string]bool
}

func newHealthState() *healthState {
	return &healthState{strikes: map[string]int{}, down: map[string]bool{}}
}

// healthClient does not follow redirects: a 301 to the canonical hostname is a
// perfectly healthy answer from an app that is very much alive, and chasing it
// would leave the panel testing whatever is at the other end instead.
var healthClient = &http.Client{
	Timeout: healthRequestTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func (s *Server) startHealthChecks() {
	go func() {
		t := time.NewTicker(healthCheckInterval)
		defer t.Stop()
		for range t.C {
			s.runHealthChecks()
		}
	}()
}

// runHealthChecks asks every running server that has a health path for a
// response, and notifies on the transitions — not on every failure.
func (s *Server) runHealthChecks() {
	defer recoverLog("runHealthChecks")
	ctx := context.Background()

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, COALESCE(health_path,'') FROM servers
		 WHERE status='running' AND COALESCE(health_path,'')<>''`)
	if err != nil {
		return
	}
	type target struct{ id, name, path string }
	var list []target
	for rows.Next() {
		var t target
		if rows.Scan(&t.id, &t.name, &t.path) == nil {
			list = append(list, t)
		}
	}
	rows.Close()

	for _, t := range list {
		port := s.firstWebHostPort(t.id)
		if port == 0 {
			continue // nothing HTTP to ask; leave it alone rather than guess
		}
		if msg := s.recordHealthResult(t.id, t.name, s.probeHealth(port, t.path)); msg != "" {
			s.notifyServer(t.id, msg)
		}
	}
}

// probeHealth returns whether the app answered. Any HTTP status counts as alive
// — including 401, 403 and 500. The question is whether something is serving,
// not whether it is happy: an app returning 500 is a different problem from an
// app that is not there, and conflating them turns this into a noisy uptime
// checker for every login-gated page on the fleet.
func (s *Server) probeHealth(port int, path string) bool {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	url := fmt.Sprintf("http://%s%s", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), path)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "yggdrasil-health")
	resp, err := healthClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// recordHealthResult advances the strike count and returns the message to send,
// or "" when there is nothing to say. Deciding and notifying are separate on
// purpose: the decision is the part with the edge cases, and it should be
// testable without a database, a notification channel, or a clock.
func (s *Server) recordHealthResult(serverID, name string, ok bool) string {
	if s.health == nil {
		return ""
	}
	s.health.mu.Lock()
	defer s.health.mu.Unlock()

	if ok {
		wasDown := s.health.down[serverID]
		s.health.strikes[serverID] = 0
		s.health.down[serverID] = false
		if wasDown {
			return "✅ " + name + " is answering again"
		}
		return ""
	}

	s.health.strikes[serverID]++
	if s.health.strikes[serverID] >= healthFailuresBeforeAlert && !s.health.down[serverID] {
		s.health.down[serverID] = true
		// Say what is odd about it, because the obvious reading — "the server is
		// down" — is the one thing that is not true. The container is up and the
		// panel will keep calling it running.
		return "🩺 " + name + " is running but not answering. Its container is up and its port is open, " +
			"so the panel still shows it as running — something inside has stopped serving."
	}
	return ""
}

// serverHealth reports the current verdict for the API: "" when no check is
// configured, "ok" or "down".
func (s *Server) serverHealth(serverID, healthPath string) string {
	if healthPath == "" || s.health == nil {
		return ""
	}
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	if s.health.down[serverID] {
		return "down"
	}
	return "ok"
}

// clearHealth forgets a server's strikes — on stop, so a deliberate stop does
// not surface as a health alert the moment it starts again.
func (s *Server) clearHealth(serverID string) {
	if s.health == nil {
		return
	}
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	delete(s.health.strikes, serverID)
	delete(s.health.down, serverID)
}
