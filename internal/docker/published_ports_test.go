package docker

import (
	"sort"
	"testing"

	"github.com/docker/go-connections/nat"
)

func keys(m nat.PortMap) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, string(p))
	}
	sort.Strings(out)
	return out
}

// The load-bearing case: one mapping, two published protocols, the SAME host
// port on both. Two rune entries could never express this — host ports are
// allocated one number at a time — so this is the only place it can happen.
func TestPublishedPortsTCPUDP(t *testing.T) {
	bindings, exposed := publishedPorts([]PortMapping{
		{HostPort: 25010, ContainerPort: 21116, Protocol: "tcp+udp"},
	})
	got := keys(bindings)
	want := []string{"21116/tcp", "21116/udp"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("bindings = %v, want %v", got, want)
	}
	if len(exposed) != 2 {
		t.Errorf("exposed = %d entries, want 2", len(exposed))
	}
	for _, p := range want {
		b := bindings[nat.Port(p)]
		if len(b) != 1 || b[0].HostPort != "25010" {
			t.Errorf("%s publishes %v, want host port 25010", p, b)
		}
	}
}

// Every other spelling must produce byte-for-byte what it produced before this
// feature existed — one entry, one protocol. A regression here would break every
// existing rune, and would not show up in the tcp+udp case above.
func TestPublishedPortsUnchangedForSingleProtocol(t *testing.T) {
	cases := []struct{ protocol, want string }{
		{"tcp", "7777/tcp"},
		{"udp", "7777/udp"},
		{"", "7777/tcp"}, // unset has always meant tcp
	}
	for _, c := range cases {
		bindings, exposed := publishedPorts([]PortMapping{
			{HostPort: 30000, ContainerPort: 7777, Protocol: c.protocol},
		})
		got := keys(bindings)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("protocol %q -> %v, want [%s]", c.protocol, got, c.want)
		}
		if len(exposed) != 1 {
			t.Errorf("protocol %q exposed %d entries, want 1", c.protocol, len(exposed))
		}
	}
}

// Several mappings must not collide or drop each other.
func TestPublishedPortsMultipleMappings(t *testing.T) {
	bindings, _ := publishedPorts([]PortMapping{
		{HostPort: 25010, ContainerPort: 21116, Protocol: "tcp+udp"},
		{HostPort: 25011, ContainerPort: 21117, Protocol: "tcp"},
		{HostPort: 25012, ContainerPort: 21118, Protocol: "udp"},
	})
	if len(bindings) != 4 {
		t.Fatalf("bindings = %v, want 4 entries", keys(bindings))
	}
}
