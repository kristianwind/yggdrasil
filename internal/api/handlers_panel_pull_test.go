package api

import "testing"

func TestNormalizeRemote(t *testing.T) {
	ok := map[string]string{
		// The tailnet case this exists for: a bare host:port must work, and must
		// default to http — a Tailscale address has no certificate.
		"100.80.130.8:8080":         "http://100.80.130.8:8080",
		"http://100.80.130.8:8080":  "http://100.80.130.8:8080",
		"https://panel.example.com": "https://panel.example.com",
		// A trailing slash would otherwise produce "…//api/servers".
		"https://panel.example.com/":    "https://panel.example.com",
		"  https://panel.example.com  ": "https://panel.example.com",
		// A path prefix (panel behind a sub-path) is preserved.
		"https://example.com/ygg/": "https://example.com/ygg",
	}
	for in, want := range ok {
		got, err := normalizeRemote(in)
		if err != nil {
			t.Errorf("normalizeRemote(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normalizeRemote(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{
		"",                   // empty
		"   ",                // blank
		"ftp://example.com",  // wrong scheme
		"file:///etc/passwd", // not fetchable, and an obvious abuse shape
		"http://",            // no host
	}
	for _, in := range bad {
		if got, err := normalizeRemote(in); err == nil {
			t.Errorf("normalizeRemote(%q) = %q, want an error", in, got)
		}
	}
}
