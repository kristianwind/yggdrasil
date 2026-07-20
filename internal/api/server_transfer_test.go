package api

import "testing"

// A server name becomes a safe download filename — no slashes, spaces or exotica
// that would break Content-Disposition or the filesystem.
func TestSafeFilename(t *testing.T) {
	cases := map[string]string{
		"Asgard":           "Asgard",
		"Dalma og Fatti":   "Dalma-og-Fatti",
		"my/evil\\name":    "my-evil-name",
		"  spaced  ":       "spaced",
		"":                 "server",
		"../../etc/passwd": "------etc-passwd",
	}
	for in, want := range cases {
		if got := safeFilename(in); got != want {
			t.Errorf("safeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}
