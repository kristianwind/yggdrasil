package api

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// A migration must keep the source's host ports so the operator's forwarding
// (game.host:PORT via NPM/tunnel/DNS) survives — reallocating only the ones that
// genuinely collide on the target, and reporting exactly those. This is the
// behaviour the pre-fix import lacked: it called allocatePort for every port and
// handed migrated servers brand-new numbers.
func TestPickTransferPorts(t *testing.T) {
	runePorts := []gameskill.Port{{Name: "game"}, {Name: "query"}, {Name: "rcon"}}
	source := map[string]int{"game": 25081, "query": 25082, "rcon": 25083}

	// Fresh, empty target: every source port is free, so all are preserved 1:1 and
	// nothing is reported as moved.
	got, moved, err := pickTransferPorts(runePorts, source, nil, 25000, 30000, func(int) bool { return true })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, source) {
		t.Errorf("fresh target: ports = %v, want them preserved as %v", got, source)
	}
	if len(moved) != 0 {
		t.Errorf("fresh target: nothing should move, got %v", moved)
	}

	// One source port (query's 25082) is already taken on the target. That one must
	// move; the other two stay put; only the collision is reported.
	taken := map[int]bool{25082: true}
	got, moved, err = pickTransferPorts(runePorts, source, taken, 25000, 30000,
		func(p int) bool { return !taken[p] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["game"] != 25081 || got["rcon"] != 25083 {
		t.Errorf("free source ports should be preserved, got %v", got)
	}
	if got["query"] == 25082 {
		t.Errorf("query's port was taken but wasn't reallocated: %v", got)
	}
	if len(moved) != 1 || moved[0] != "query 25082→"+strconv.Itoa(got["query"]) {
		t.Errorf("moved = %v, want exactly the query collision", moved)
	}

	// An explicit source port outside the auto-allocation range is still honoured —
	// preserving the operator's chosen port is the point of a migration.
	got, moved, err = pickTransferPorts([]gameskill.Port{{Name: "web"}},
		map[string]int{"web": 40000}, nil, 25000, 30000, func(int) bool { return true })
	if err != nil || got["web"] != 40000 || len(moved) != 0 {
		t.Errorf("out-of-range source port should be preserved: got=%v moved=%v err=%v", got, moved, err)
	}
}

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
