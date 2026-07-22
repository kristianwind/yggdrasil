package api

import "regexp"

// ansiRe matches ANSI/VT100 escape sequences — CSI sequences like the SGR colour
// codes SteamCMD and game servers emit (\x1b[0m, \x1b[1;32m, …). The ESC byte is
// invisible in a browser log view, so an unstripped reset shows up as a literal
// "[0m" in front of a line ("[0mOK", "[0minstalled").
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// stripANSI removes ANSI escape sequences so streamed log output reads cleanly in
// the panel, which renders plain text rather than interpreting terminal colours.
func stripANSI(s string) string {
	if !containsESC(s) {
		return s // fast path: most lines have no escapes
	}
	return ansiRe.ReplaceAllString(s, "")
}

func containsESC(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			return true
		}
	}
	return false
}
