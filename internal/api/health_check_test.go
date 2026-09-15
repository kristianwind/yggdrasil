package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// probeHealth answers one question: did something serve a response. Not whether
// it served a good one. An app returning 500 is a different problem from an app
// that is not there, and treating them alike turns this into a noisy uptime
// checker for every login-gated page on the fleet.
func TestProbeHealthAcceptsAnyHTTPStatus(t *testing.T) {
	s := &Server{}
	for _, code := range []int{200, 301, 401, 403, 500, 503} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if code == 301 {
				w.Header().Set("Location", "https://example.com/")
			}
			w.WriteHeader(code)
		}))
		port := portOf(t, srv.URL)
		if !s.probeHealth(port, "/") {
			t.Errorf("HTTP %d should count as alive — something is serving", code)
		}
		srv.Close()
	}
}

// The failure this exists for: a port that accepts a TCP connection with
// nothing behind it. That is what Docker's userland proxy leaves when a
// container is up but its app never started, and it is why dialling the port
// cannot tell a live app from a dead one.
func TestProbeHealthFailsOnAPortThatAcceptsButNeverAnswers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Accept and say nothing — exactly the docker-proxy shape.
			_ = c
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	// A plain TCP dial succeeds here, which is the whole point.
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("the port must be connectable for this test to mean anything: %v", err)
	}
	c.Close()

	s := &Server{}
	if s.probeHealth(port, "/") {
		t.Error("a port that accepts and never answers must not count as healthy — that is the exact case this check exists for")
	}
}

func TestProbeHealthNormalisesThePath(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
	}))
	defer srv.Close()
	s := &Server{}
	// Written without a leading slash, which is what people type.
	s.probeHealth(portOf(t, srv.URL), "healthz")
	if got != "/healthz" {
		t.Errorf("path = %q, want %q", got, "/healthz")
	}
}

// Transitions are reported, not every failure — and the first two failures say
// nothing at all, because one bad moment is a restart, not an outage.
func TestHealthAlertsOnlyOnTheCrossing(t *testing.T) {
	s := &Server{health: newHealthState()}
	const id = "srv-1"

	for i := 1; i < healthFailuresBeforeAlert; i++ {
		if msg := s.recordHealthResult(id, "Site", false); msg != "" {
			t.Fatalf("failure %d of %d must say nothing, got %q", i, healthFailuresBeforeAlert, msg)
		}
		if s.serverHealth(id, "/") != "ok" {
			t.Fatalf("failure %d of %d should not have marked it down yet", i, healthFailuresBeforeAlert)
		}
	}
	msg := s.recordHealthResult(id, "Site", false)
	if msg == "" {
		t.Fatalf("the %dth consecutive failure must report", healthFailuresBeforeAlert)
	}
	if !strings.Contains(msg, "running but not answering") {
		t.Errorf("the alert must not read as a plain outage — the container IS up: %q", msg)
	}
	if s.serverHealth(id, "/") != "down" {
		t.Fatalf("after %d consecutive failures it must read down", healthFailuresBeforeAlert)
	}
	// Still down on the next failure, but silent — one crossing, one message.
	if again := s.recordHealthResult(id, "Site", false); again != "" {
		t.Errorf("a continuing outage must not keep paging: %q", again)
	}

	// One good answer clears it, and the strike count with it — otherwise a
	// server that flaps once an hour would alert on its second blip forever.
	if rec := s.recordHealthResult(id, "Site", true); !strings.Contains(rec, "answering again") {
		t.Errorf("recovery must be reported, got %q", rec)
	}
	if s.serverHealth(id, "/") != "ok" {
		t.Fatal("a successful probe must clear the down state")
	}
	if msg := s.recordHealthResult(id, "Site", false); msg != "" {
		t.Errorf("strikes must reset on success, so a single later failure does not re-alert: %q", msg)
	}
}

// No path configured means no opinion. Most servers have no HTTP endpoint worth
// asking, and a check that guessed would page people about game servers that
// were never going to answer.
func TestHealthIsSilentWithoutAPath(t *testing.T) {
	s := &Server{health: newHealthState()}
	if got := s.serverHealth("srv-x", ""); got != "" {
		t.Errorf("health = %q, want empty when no path is configured", got)
	}
}

// A deliberate stop forgets the strikes, so restarting does not immediately
// report a recovery for something nobody was told had failed.
func TestClearHealthForgetsStrikes(t *testing.T) {
	s := &Server{health: newHealthState()}
	for i := 0; i <= healthFailuresBeforeAlert; i++ {
		s.recordHealthResult("srv-2", "Site", false)
	}
	if s.serverHealth("srv-2", "/") != "down" {
		t.Fatal("precondition: it should be down")
	}
	s.clearHealth("srv-2")
	if got := s.serverHealth("srv-2", "/"); got != "ok" {
		t.Errorf("health = %q, want a clean slate after clearHealth", got)
	}
}

func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	_, p, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	if err != nil {
		t.Fatalf("split %q: %v", rawURL, err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatalf("port %q: %v", p, err)
	}
	return n
}
