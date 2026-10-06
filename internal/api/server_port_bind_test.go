package api

import (
	"testing"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// Which interface each of a server's ports is published on.
//
// The pair matters more than either half: bind the game port to loopback and
// nobody can play, bind the admin port to every interface and on a machine with
// a public address the console is answering the internet. The second is what it
// did.
func TestServerPortMappingsBindAdminToLoopback(t *testing.T) {
	gs := &gameskill.Gameskill{
		Ports: []gameskill.Port{
			{Name: "game", Default: 25565, Protocol: "tcp"},
			{Name: "rcon", Default: 25575, Protocol: "tcp"},
			{Name: "query", Default: 25565, Protocol: "udp"},
		},
	}
	ports := map[string]int{"game": 25565, "rcon": 25575, "query": 25566}

	got := map[string]string{}
	for _, m := range serverPortMappings(gs, ports) {
		switch m.HostPort {
		case 25565:
			got["game"] = m.HostIP
		case 25575:
			got["rcon"] = m.HostIP
		case 25566:
			got["query"] = m.HostIP
		}
	}

	if got["rcon"] != "127.0.0.1" {
		t.Errorf("rcon published on %q, want 127.0.0.1 — an RCON console reachable off this host "+
			"is somebody else's console", got["rcon"])
	}
	if got["game"] != "" {
		t.Errorf("the game port is bound to %q — players cannot reach it", got["game"])
	}
	// Query is how server browsers find a server. Treating it as administrative
	// because it sits next to RCON would delist every server on the panel.
	if got["query"] != "" {
		t.Errorf("the query port is bound to %q — server lists could no longer see it", got["query"])
	}
}

// The forwarding paths and the bind must agree on what "admin" means. They did
// not: one knew rcon was special and the other did not, which is the whole bug.
func TestAdminPortDefinitionIsShared(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"rcon", true},
		{"RCON", true}, // runes are not consistent about case
		{"game", false},
		{"query", false},
		{"web", false},
	} {
		if got := isAdminPort(tc.name); got != tc.want {
			t.Errorf("isAdminPort(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}

	// And the forwarding path must read the same function, or the two drift back
	// apart exactly as they had.
	gs := &gameskill.Gameskill{Ports: []gameskill.Port{
		{Name: "rcon", Default: 25575, Protocol: "tcp"},
	}}
	m := serverPortMappings(gs, map[string]int{"rcon": 25575})
	if len(m) != 1 || m[0].HostIP != "127.0.0.1" {
		t.Fatalf("rcon mapping = %+v", m)
	}
}
