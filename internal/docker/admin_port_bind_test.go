package docker

import (
	"testing"

	"github.com/docker/go-connections/nat"
)

// An admin port must be published on loopback, and a game port must not.
//
// The bug was not that RCON was forwarded — it never was. serverPortProtos
// flags it so UPnP and UniFi skip it, and the comment there says "reachable
// locally but never WAN-forwarded". It was that skipping the router does
// nothing about the BIND: with no HostIP, Docker publishes on 0.0.0.0 and
// writes its own iptables rules ahead of the host firewall, so on a machine
// with a public address the console answered the internet from the moment the
// container started. Behind NAT nobody could see it, which is why it stood.
func TestPublishedPortsBindAdminToLoopbackOnly(t *testing.T) {
	bindings, exposed := publishedPorts([]PortMapping{
		{HostPort: 25565, ContainerPort: 25565, Protocol: "tcp"},                      // game
		{HostPort: 25575, ContainerPort: 25575, Protocol: "tcp", HostIP: "127.0.0.1"}, // rcon
		{HostPort: 27015, ContainerPort: 27015, Protocol: "tcp+udp"},                  // both protocols
	})

	if len(exposed) != 4 {
		t.Fatalf("exposed %d ports, want 4 (tcp+udp counts twice)", len(exposed))
	}

	for _, tc := range []struct {
		port   string
		hostIP string
		why    string
	}{
		{"25565/tcp", "", "a game port has to answer every interface or nobody can play"},
		{"25575/tcp", "127.0.0.1", "RCON is a console; off-host it is somebody else's console"},
		{"27015/tcp", "", "tcp+udp must carry the bind through to both halves"},
		{"27015/udp", "", "tcp+udp must carry the bind through to both halves"},
	} {
		b, ok := bindings[nat.Port(tc.port)]
		if !ok || len(b) != 1 {
			t.Fatalf("%s: no binding", tc.port)
		}
		if b[0].HostIP != tc.hostIP {
			t.Errorf("%s bound to HostIP %q, want %q — %s", tc.port, b[0].HostIP, tc.hostIP, tc.why)
		}
	}
}
