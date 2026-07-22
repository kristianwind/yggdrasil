package api

import "testing"

func TestStripANSI(t *testing.T) {
	cases := map[string]string{
		"\x1b[0mOK":                 "OK",                          // the reported "[0mOK"
		"\x1b[0minstalled":          "installed",                   // the reported "[0minstalled"
		"\x1b[1;32mSuccess!\x1b[0m": "Success!",                    // bold-green + reset
		"plain line":                "plain line",                  // untouched
		"":                          "",                            // empty
		"progress: \x1b[Kdone":      "progress: done",              // CSI K (erase line)
		"Update state (0x61) \x1b[0mdownloading, progress: 42.5":     "Update state (0x61) downloading, progress: 42.5",
	}
	for in, want := range cases {
		if got := stripANSI(in); got != want {
			t.Errorf("stripANSI(%q) = %q, want %q", in, got, want)
		}
	}
}
