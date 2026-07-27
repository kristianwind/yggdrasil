package cloudflare

import "testing"

func TestAccessRuleTarget(t *testing.T) {
	cases := map[string]string{
		"149.36.51.138": "ip",
		"8.8.8.8":       "ip",
		"2606:4700::1":  "ip6",
		"fe80::1":       "ip6",
	}
	for ip, want := range cases {
		if got := accessRuleTarget(ip); got != want {
			t.Errorf("accessRuleTarget(%q) = %q, want %q", ip, got, want)
		}
	}
}
