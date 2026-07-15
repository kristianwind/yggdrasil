package notify

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// blackHoleSMTP accepts the TCP connection and then says nothing at all — the
// failure mode a firewall, a wrong port, or a wedged mail server produces. It is
// not the same as a refused connection, which fails fast on its own.
func blackHoleSMTP(t *testing.T) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold it open, never send the 220 greeting.
			t.Cleanup(func() { c.Close() })
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

// An SMTP host that accepts and then stalls must not hang forever.
//
// smtp.SendMail has no timeout: it dials without a deadline and blocks on the
// greeting. Every other channel goes through an http.Client with a 10s timeout.
// This one leaked a goroutine per background notification, and hung the request
// outright for the synchronous "Test" button — which has no server-side timeout
// to rescue it.
func TestSendEmailTimesOutOnASilentServer(t *testing.T) {
	host, port := blackHoleSMTP(t)

	// Keep the test quick: the point is that a deadline exists and fires, not its
	// exact value.
	orig := smtpDeadline
	smtpDeadline = 400 * time.Millisecond
	t.Cleanup(func() { smtpDeadline = orig })

	cfg := Config{
		Type: "email", Host: host, Port: port,
		From: "a@example.com", To: "b@example.com",
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- Send(cfg, "hello") }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a silent SMTP server returned success")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("took %v to give up — the deadline isn't bounding the read", elapsed)
		}
		t.Logf("gave up after %v: %v", time.Since(start).Round(time.Millisecond), err)
	case <-time.After(5 * time.Second):
		t.Fatal("Send hung on a silent SMTP server — this is the bug: no deadline on the connection")
	}
}

// A refused connection must still fail fast and say something useful.
func TestSendEmailFailsOnRefusedConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing is listening now

	cfg := Config{Type: "email", Host: "127.0.0.1", Port: port, From: "a@x.com", To: "b@x.com"}
	err = Send(cfg, "hello")
	if err == nil {
		t.Fatal("expected an error for a refused connection")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(port)) && !strings.Contains(err.Error(), "refused") {
		t.Logf("error was: %v", err) // informational; the message shape isn't contractual
	}
}

// Missing fields are rejected before any network call.
func TestSendEmailRequiresHostFromTo(t *testing.T) {
	if err := Send(Config{Type: "email"}, "x"); err == nil {
		t.Error("expected an error when host/from/to are missing")
	}
}
