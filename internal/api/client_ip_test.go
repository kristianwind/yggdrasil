package api

import (
	"net/http"
	"testing"
)

func TestResolveClientIP(t *testing.T) {
	cases := []struct {
		name, remote string
		xff          []string
		realIP, want string
	}{
		{"direct, no header: the port is dropped", "203.0.113.9:51234", nil, "", "203.0.113.9"},
		{"direct from the internet: headers are the caller's own words", "203.0.113.9:51234", []string{"10.0.0.1"}, "127.0.0.1", "203.0.113.9"},
		{"behind a local proxy", "127.0.0.1:4000", []string{"198.51.100.7"}, "", "198.51.100.7"},
		{"a spoofed entry in front of the real one", "127.0.0.1:4000", []string{"1.2.3.4, 198.51.100.7"}, "", "198.51.100.7"},
		{"Cloudflare in front of a LAN proxy", "192.168.1.10:4000", []string{"1.2.3.4, 198.51.100.7, 172.64.1.1"}, "", "198.51.100.7"},
		{"split across two header lines", "192.168.1.10:4000", []string{"1.2.3.4", "198.51.100.7"}, "", "198.51.100.7"},
		{"a LAN client behind the proxy", "192.168.1.10:4000", []string{"192.168.1.50"}, "", "192.168.1.50"},
		{"garbage stops the walk", "127.0.0.1:4000", []string{"198.51.100.7, <script>"}, "", "127.0.0.1"},
		{"X-Real-IP only", "127.0.0.1:4000", nil, "198.51.100.7", "198.51.100.7"},
		{"IPv6 peer", "[2001:db8::1]:443", []string{"10.0.0.1"}, "", "2001:db8::1"},
		{"no port at all", "203.0.113.9", nil, "", "203.0.113.9"},
	}
	for _, c := range cases {
		h := http.Header{}
		for _, v := range c.xff {
			h.Add("X-Forwarded-For", v)
		}
		if c.realIP != "" {
			h.Set("X-Real-IP", c.realIP)
		}
		h.Set("True-Client-IP", "6.6.6.6")
		if got := resolveClientIP(c.remote, h); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// The limiter is keyed on what clientIP leaves behind. Before, a direct
// connection's key carried its source port, so each new connection started a
// fresh count.
func TestLoginLimiterKeyDoesNotChangeWithTheSourcePort(t *testing.T) {
	a := resolveClientIP("203.0.113.9:40001", http.Header{})
	b := resolveClientIP("203.0.113.9:40002", http.Header{})
	if a != b {
		t.Fatalf("two connections from one address are two limiter keys: %q and %q", a, b)
	}
}
