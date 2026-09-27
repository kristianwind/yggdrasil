package gameskill

import (
	"strings"
	"testing"
)

// The table is the point: every spelling a rune can contain, and what each of
// the two questions consumers ask returns for it. A single-case test here would
// pass over the one row that matters (tcp+udp) being absent.
func TestPortProtocols(t *testing.T) {
	cases := []struct {
		protocol string
		want     []string
		hasTCP   bool
	}{
		{"tcp", []string{"tcp"}, true},
		{"udp", []string{"udp"}, false},
		{"tcp+udp", []string{"tcp", "udp"}, true},
		{"", []string{"tcp"}, true}, // unset has always meant tcp
	}
	for _, c := range cases {
		p := Port{Name: "x", Default: 7777, Protocol: c.protocol}
		got := p.Protocols()
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("Protocols() for %q = %v, want %v", c.protocol, got, c.want)
		}
		if p.HasTCP() != c.hasTCP {
			t.Errorf("HasTCP() for %q = %v, want %v", c.protocol, p.HasTCP(), c.hasTCP)
		}
	}
}

// Only one spelling of the pair is accepted. "udp+tcp" means the same thing to a
// reader and nothing to the parser, and allowing both would mean every future
// grep for tcp+udp has a second string to remember.
// validGS is a rune that passes validation on everything EXCEPT the port under
// test. Without it the rejection test below passes for the wrong reason: the
// first fixture failed on a missing startup.command, so every protocol looked
// rejected and the test would have stayed green with the check deleted.
func validGS(protocol string) *Gameskill {
	return &Gameskill{
		ID: "x", Name: "X", Version: 1,
		Docker:  Docker{Image: "alpine"},
		Startup: Startup{Command: "sleep 1"},
		Ports:   []Port{{Name: "game", Default: 7777, Protocol: protocol}},
	}
}

func TestValidateRejectsBadProtocol(t *testing.T) {
	// The fixture is sound apart from the protocol, so assert the error is ABOUT
	// the protocol. "some error occurred" is the same green as no check at all.
	if err := validate(validGS("tcp")); err != nil {
		t.Fatalf("fixture is not otherwise valid, so this test proves nothing: %v", err)
	}
	for _, proto := range []string{"udp+tcp", "tcp,udp", "both", "TCP+UDP", "tcp+udp+tcp", ""} {
		err := validate(validGS(proto))
		if err == nil {
			t.Errorf("validate accepted protocol %q, want rejected", proto)
			continue
		}
		if !strings.Contains(err.Error(), "protocol") {
			t.Errorf("protocol %q rejected for an unrelated reason: %v", proto, err)
		}
	}
}

func TestValidateAcceptsTCPUDP(t *testing.T) {
	if err := validate(validGS("tcp+udp")); err != nil {
		t.Fatalf("validate rejected tcp+udp: %v", err)
	}
}
