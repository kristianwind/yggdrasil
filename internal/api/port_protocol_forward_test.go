package api

import (
	"strings"
	"testing"
)

// UPnP opens and closes one mapping per protocol, and the two must agree: a pair
// opened as two mappings that is closed as one leaves a UDP hole in the router
// after the server is deleted. Both paths call upnpProtos, so the test is that
// the expansion is right for every spelling.
func TestUPnPProtos(t *testing.T) {
	cases := map[string]string{
		"tcp":     "tcp",
		"udp":     "udp",
		"tcp+udp": "tcp,udp",
		"":        "tcp", // unset has always meant tcp
	}
	for in, want := range cases {
		if got := strings.Join(upnpProtos(in), ","); got != want {
			t.Errorf("upnpProtos(%q) = %q, want %q", in, got, want)
		}
	}
}

// UniFi forwards the pair as a single tcp_udp rule. Emitting two rules would
// work, but both would carry the same generated name and the same [ygg:] tag for
// one published port — which is exactly the duplicate that makes a router's
// forward list unauditable.
func TestUnifiProto(t *testing.T) {
	cases := map[string]string{
		"tcp":      "tcp",
		"udp":      "udp",
		"UDP":      "udp",
		"tcp+udp":  "tcp_udp",
		"":         "tcp",
		"nonsense": "tcp", // validation rejects it upstream; never emit a proto UniFi would refuse
	}
	for in, want := range cases {
		if got := unifiProto(in); got != want {
			t.Errorf("unifiProto(%q) = %q, want %q", in, got, want)
		}
	}
}
