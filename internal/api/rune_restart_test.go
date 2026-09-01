package api

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// Which servers a rune restart touches, and — more importantly — which it does
// not. Starting something the operator had deliberately stopped would be a
// surprise nobody asked for, and a stopped server picks the rune up whenever
// someone starts it anyway.
func TestRuneServersTakesRunningOnesOnly(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()

	add := func(name, rune, status string, installed int) {
		t.Helper()
		if _, err := s.db.Exec(
			`INSERT INTO servers (id, name, gameskill_id, status, installed, data_dir)
			 VALUES (?,?,?,?,?,?)`,
			uuid.New().String(), name, rune, status, installed, "/tmp/"+name); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	add("survival", "minecraft-java", "running", 1)
	add("creative", "minecraft-java", "starting", 1)  // mid-boot still counts
	add("retired", "minecraft-java", "stopped", 1)    // deliberately off
	add("half-built", "minecraft-java", "running", 0) // never finished installing
	add("chernarus", "dayz", "running", 1)            // a different rune entirely

	got := map[string]bool{}
	for _, x := range s.runeServers(ctx, "minecraft-java") {
		got[x.Name] = true
	}

	for _, want := range []string{"survival", "creative"} {
		if !got[want] {
			t.Errorf("%s uses the rune and is up — it must be restarted", want)
		}
	}
	if got["retired"] {
		t.Error("a stopped server must be left alone; restarting it would start something the operator stopped")
	}
	if got["half-built"] {
		t.Error("an uninstalled server has no container to recreate")
	}
	if got["chernarus"] {
		t.Error("a server on a different rune must not be swept up")
	}
}

// A double-click must not recreate the same eight containers twice over.
func TestOnlyOneRuneRestartSweepAtATime(t *testing.T) {
	st := newRuneRestartState()
	if !st.begin("minecraft-java") {
		t.Fatal("the first sweep must start")
	}
	if st.begin("minecraft-java") {
		t.Error("a second sweep of the same rune must be refused while one is running")
	}
	if !st.begin("dayz") {
		t.Error("a different rune is unrelated and must not be blocked")
	}
	st.end("minecraft-java")
	if !st.begin("minecraft-java") {
		t.Error("once the sweep finishes another must be allowed")
	}
}
