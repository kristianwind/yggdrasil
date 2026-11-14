package api

import (
	"net/http/httptest"
	"testing"
)

func TestSameOriginHostCountsThePort(t *testing.T) {
	cases := []struct {
		name, origin, host string
		proxied, want      bool
	}{
		{"same host and port", "http://192.168.1.5:8080", "192.168.1.5:8080", false, true},
		{"another port on the same host — one of the panel's own servers", "http://192.168.1.5:8081", "192.168.1.5:8080", false, false},
		{"the same, with forwarding headers present", "http://192.168.1.5:8081", "192.168.1.5:8080", true, false},
		{"another host", "https://evil.example", "panel.example", true, false},
		{"behind a proxy on the default port", "https://panel.example", "panel.example", true, true},
		{"default port, no proxy", "http://panel.lan", "panel.lan", false, true},
		{"Host without a port, Origin on another, no proxy", "http://panel.lan:8081", "panel.lan", false, false},
		{"published on :8443 by a proxy that drops the port from Host", "https://panel.example:8443", "panel.example", true, true},
		{"proxy forwards Host with :443", "https://panel.example", "panel.example:443", true, true},
		{"explicit default port in Origin", "http://panel.lan:80", "panel.lan", false, true},
		{"IPv6", "http://[::1]:8080", "[::1]:8080", false, true},
		{"IPv6, other port", "http://[::1]:9000", "[::1]:8080", false, false},
		{"case", "http://Panel.LAN:8080", "panel.lan:8080", false, true},
		{"not a URL", "null", "panel.lan:8080", false, false},
	}
	for _, c := range cases {
		if got := sameOriginHost(c.origin, c.host, c.proxied); got != c.want {
			t.Errorf("%s: sameOriginHost(%q, %q, %v) = %v, want %v", c.name, c.origin, c.host, c.proxied, got, c.want)
		}
	}
}

func TestConsoleWebSocketRefusesAPageOnAnotherPort(t *testing.T) {
	r := httptest.NewRequest("GET", "http://192.168.1.5:8080/api/servers/x/console", nil)
	r.Header.Set("Origin", "http://192.168.1.5:8081")
	if wsUpgrader.CheckOrigin(r) {
		t.Fatal("a page served from another port may open the console")
	}
	r.Header.Set("Origin", "http://192.168.1.5:8080")
	if !wsUpgrader.CheckOrigin(r) {
		t.Fatal("the panel's own page may not open the console")
	}
}
