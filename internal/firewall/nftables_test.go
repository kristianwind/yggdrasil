package firewall

import (
	"net"
	"testing"
)

func TestSetFor(t *testing.T) {
	cases := map[string]string{
		"149.36.51.138": set4,
		"8.8.8.8":       set4,
		"2606:4700::1":  set6,
		"fe80::1":       set6,
	}
	for ip, want := range cases {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			t.Fatalf("bad test IP %q", ip)
		}
		if got := setFor(parsed); got != want {
			t.Errorf("setFor(%q) = %q, want %q", ip, got, want)
		}
	}
}
