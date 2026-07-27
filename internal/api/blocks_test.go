package api

import "testing"

func TestCheckBlockable_RefusesProtected(t *testing.T) {
	// Addresses we must never block: invalid, loopback, private, link-local,
	// Tailscale/CGNAT, and Cloudflare's own edge.
	refuse := []string{
		"",                // empty
		"not-an-ip",       // garbage
		"999.1.1.1",       // out of range
		"127.0.0.1",       // loopback
		"::1",             // loopback v6
		"10.0.0.5",        // RFC1918
		"172.16.4.9",      // RFC1918
		"192.168.1.50",    // RFC1918
		"169.254.10.1",    // link-local
		"100.92.81.54",    // Tailscale CGNAT (100.64/10)
		"fe80::1",         // v6 link-local
		"fc00::1",         // v6 ULA
		"104.16.0.1",      // Cloudflare (104.16.0.0/13)
		"172.64.0.1",      // Cloudflare (172.64.0.0/13)
		"2606:4700::1111", // Cloudflare v6
	}
	for _, ip := range refuse {
		if _, err := checkBlockable(ip); err == nil {
			t.Errorf("checkBlockable(%q) = allowed, want refused", ip)
		}
	}
}

func TestCheckBlockable_AllowsPublic(t *testing.T) {
	// Real attacker IPs seen in the wild should be blockable.
	allow := map[string]string{
		"149.36.51.138":   "149.36.51.138",
		"37.211.153.143":  "37.211.153.143",
		"118.189.242.162": "118.189.242.162",
		"185.29.76.1":     "185.29.76.1",
		" 8.8.8.8 ":       "8.8.8.8", // trimmed
		"2001:db8::1":     "2001:db8::1",
	}
	for in, want := range allow {
		got, err := checkBlockable(in)
		if err != nil {
			t.Errorf("checkBlockable(%q) refused: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("checkBlockable(%q) = %q, want %q", in, got, want)
		}
	}
}
