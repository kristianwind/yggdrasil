package api

import (
	"testing"

	"github.com/kristianwind/yggdrasil/internal/unifi"
)

func rule(name, fwd string) unifi.PortForward {
	return unifi.PortForward{ID: name, Name: name, Fwd: fwd, Proto: "tcp"}
}

// 🔴 The property everything else depends on: several panels share one
// controller, so a rule this panel does not recognise is very likely another
// panel's. Deleting by "I don't know this tag" alone takes out production on a
// machine the operator was not even looking at.
func TestNeverTouchesAnotherHostsRules(t *testing.T) {
	rules := []unifi.PortForward{
		rule("Yggdrasil: Venus forever [ygg:f5e5ac1d]", "192.168.1.111"),
		rule("Yggdrasil: Verdande [ygg:e3a8f386]", "192.168.1.164"), // another panel's
		rule("Yggdrasil: 25079/udp", "192.168.1.158"),               // another panel's legacy
	}
	got := unifiOrphans(rules, map[string]bool{}, "192.168.1.111")
	for _, g := range got {
		if g.Fwd != "192.168.1.111" {
			t.Fatalf("would delete a rule pointing at %s — that is another panel's server", g.Fwd)
		}
	}
	if len(got) != 1 || got[0].Name != "Yggdrasil: Venus forever [ygg:f5e5ac1d]" {
		t.Errorf("got %v", got)
	}
}

// The legacy rules are the whole reason this exists: no tag, so nothing in the
// panel can ever match them, so they accumulate until one holds a port a new
// server wants and loses the race invisibly.
func TestUntaggedLegacyRulesAreOrphans(t *testing.T) {
	rules := []unifi.PortForward{rule("Yggdrasil: 25079/udp", "10.0.0.5")}
	got := unifiOrphans(rules, map[string]bool{"f5e5ac1d": true}, "10.0.0.5")
	if len(got) != 1 {
		t.Fatalf("an untagged rule on this host must be collectable, got %v", got)
	}
	if _, tagged := unifiTagID(got[0].Name); tagged {
		t.Error("that rule has no tag; unifiTagID should say so")
	}
}

// A server that still exists keeps its rule — including a stopped one. The
// operator decides when a stopped server goes; a cleanup must not pre-empt it.
func TestKnownServersAreLeftAlone(t *testing.T) {
	rules := []unifi.PortForward{
		rule("Yggdrasil: Jotunheim [ygg:470bb595]", "10.0.0.5"),
		rule("Yggdrasil: Ghost [ygg:deadbeef]", "10.0.0.5"),
	}
	got := unifiOrphans(rules, map[string]bool{"470bb595": true}, "10.0.0.5")
	if len(got) != 1 || got[0].Name != "Yggdrasil: Ghost [ygg:deadbeef]" {
		t.Errorf("got %v", got)
	}
}

// Rules the panel did not create are not its business, whoever they point at.
func TestForeignRulesAreNeverCollected(t *testing.T) {
	rules := []unifi.PortForward{
		rule("Plex", "10.0.0.5"),
		rule("Home Assistant", "10.0.0.5"),
	}
	if got := unifiOrphans(rules, map[string]bool{}, "10.0.0.5"); len(got) != 0 {
		t.Errorf("only rules the panel named are candidates, got %v", got)
	}
}

// If the panel cannot work out its own address it cannot scope the delete, and
// an unscoped delete is the dangerous one. Fail closed.
func TestNoAddressDeletesNothing(t *testing.T) {
	rules := []unifi.PortForward{rule("Yggdrasil: 25079/udp", "10.0.0.5")}
	if got := unifiOrphans(rules, map[string]bool{}, "  "); len(got) != 0 {
		t.Errorf("with no address to scope by, nothing may be deleted, got %v", got)
	}
}

func TestUnifiTagID(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"Yggdrasil: Venus forever [ygg:f5e5ac1d]", "f5e5ac1d", true},
		{"Yggdrasil: 25079/udp", "", false},
		{"Yggdrasil: broken [ygg:nope", "", false},
	} {
		got, ok := unifiTagID(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("unifiTagID(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
