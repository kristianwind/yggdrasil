package api

import "testing"

func TestKvasirClampMemory(t *testing.T) {
	cases := []struct {
		name                             string
		proposed, current, hostTotal, mb int64
		ok                               bool
	}{
		{"raise within 2x", 4096, 2048, 32000, 4096, true},
		{"proposal over 2x is clamped to 2x", 8192, 2048, 32000, 4096, true},
		{"proposal over 80% host is clamped to host cap", 40000, 20000, 32000, 25600, true},
		{"no host info uses 2x cap", 8192, 2048, 0, 4096, true},
		{"unlimited current cannot auto-apply", 4096, 0, 32000, 0, false},
		{"equal to current is not a raise", 4096, 4096, 32000, 0, false},
		{"below current is not a raise", 2048, 4096, 32000, 0, false},
		{"current already above host cap declines", 40000, 30000, 32000, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := kvasirClampMemory(c.proposed, c.current, c.hostTotal)
			if ok != c.ok || got != c.mb {
				t.Fatalf("kvasirClampMemory(%d,%d,%d) = (%d,%v), want (%d,%v)",
					c.proposed, c.current, c.hostTotal, got, ok, c.mb, c.ok)
			}
		})
	}
}
