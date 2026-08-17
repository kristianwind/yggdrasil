package api

import (
	"fmt"
	"net"
	"os"
	"testing"
)

// A privileged port must be assignable. The panel runs unprivileged, so it cannot
// bind one to test it, and treating "cannot test" as "in use" made 445, 80, 443
// and 2049 permanently unassignable — the ports where an exact number is the whole
// requirement (Windows cannot be told to use anything but 445 for SMB).
func TestPrivilegedPortIsAssignable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: binding 445 succeeds outright, so there is nothing to distinguish")
	}
	if !hostPortAvailable(445) {
		t.Error("hostPortAvailable(445) = false as an unprivileged user; EACCES means untestable, not taken")
	}
}

// The ordinary case must keep working: a port something is really listening on is
// reported unavailable.
func TestBoundPortIsUnavailable(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if hostPortAvailable(port) {
		t.Errorf("hostPortAvailable(%d) = true while a listener holds it", port)
	}
	_ = fmt.Sprint(port)
}
