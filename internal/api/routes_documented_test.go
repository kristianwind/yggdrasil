package api

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The API reference is the only description of this surface anyone outside the
// code can read, and it drifts the way all such documents drift: silently, one
// endpoint at a time, and nobody notices until somebody trusts it.
//
// Measured on 2026-09-02: 53 of 262 registered routes had no entry at all. They
// were frozen in a list here so the gap could not grow, and then written up —
// each one read from its handler rather than guessed at, because a plausible
// wrong sentence in a reference is worse than a missing one. The list is empty
// now, and the test is what keeps it that way.
//
// Two rules, and the second matters as much as the first:
//
//   - a route not in the list below MUST appear in the reference, so a new
//     endpoint cannot ship undocumented;
//   - a route IN the list that has since been documented must be REMOVED from
//     it, so the list can only ever shrink and cannot quietly become fiction.
//
// If you add to this list, put the reason next to it. An empty list is the
// normal state, not an aspiration.
var knownUndocumentedRoutes = []string{}

func TestEveryRouteIsDocumented(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	ref, err := os.ReadFile("../../docs/reference/api.md")
	if err != nil {
		t.Fatalf("read api.md: %v", err)
	}
	doc := string(ref)

	known := map[string]bool{}
	for _, p := range knownUndocumentedRoutes {
		known[p] = true
	}

	re := regexp.MustCompile(`r\.(?:Get|Post|Put|Delete|Patch)\("(/api/[^"]+)"`)
	seen := map[string]bool{}
	var undocumented []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		path := m[1]
		if seen[path] {
			continue
		}
		seen[path] = true
		if strings.Contains(doc, path) {
			continue
		}
		if known[path] {
			continue
		}
		undocumented = append(undocumented, path)
	}

	if len(undocumented) > 0 {
		sort.Strings(undocumented)
		t.Errorf("these routes are registered but absent from docs/reference/api.md:\n  %s\n\n"+
			"Add a row to the reference. If you genuinely cannot yet, add the path to "+
			"knownUndocumentedRoutes with a reason — but that list is meant to shrink.",
			strings.Join(undocumented, "\n  "))
	}

	// The ratchet closing behind you: anything on the frozen list that has since
	// been written up must leave the list, or it silently re-permits a gap that no
	// longer exists and the count stops meaning anything.
	var stale []string
	for _, p := range knownUndocumentedRoutes {
		if strings.Contains(doc, p) {
			stale = append(stale, p)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("these are documented now and must be removed from knownUndocumentedRoutes:\n  %s",
			strings.Join(stale, "\n  "))
	}
}
