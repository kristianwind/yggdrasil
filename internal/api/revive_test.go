package api

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kristianwind/yggdrasil/internal/docker"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

func TestReviveBudgetIsPerServerAndSlides(t *testing.T) {
	var r reviveState
	t0 := time.Now()
	for i := 1; i <= reviveBudget; i++ {
		if n, ok := r.take("a", t0.Add(time.Duration(i)*time.Minute)); !ok || n != i {
			t.Fatalf("take %d = %d,%v", i, n, ok)
		}
	}
	if n, ok := r.take("a", t0.Add(10*time.Minute)); ok || n != reviveBudget {
		t.Errorf("over budget: take = %d,%v, want %d,false", n, ok, reviveBudget)
	}
	if _, ok := r.take("b", t0.Add(10*time.Minute)); !ok {
		t.Error("another server's budget was spent by the first")
	}
	// An hour after the first revival, one is free again.
	if n, ok := r.take("a", t0.Add(reviveWindow+90*time.Second)); !ok || n != reviveBudget {
		t.Errorf("after the window: take = %d,%v, want %d,true", n, ok, reviveBudget)
	}
}

func TestMatchesStopCommand(t *testing.T) {
	gs := &gameskill.Gameskill{}
	gs.Startup.Stop = "stop"
	for cmd, want := range map[string]bool{"stop": true, "/stop": true, " STOP ": true, "say stop": false, "stopx": false} {
		if got := matchesStopCommand(gs, cmd); got != want {
			t.Errorf("matchesStopCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
	if matchesStopCommand(&gameskill.Gameskill{}, "stop") {
		t.Error("a rune with no stop command matched one")
	}
}

func TestReviveCandidate(t *testing.T) {
	s := testServer(t)
	s.db.Exec("INSERT INTO servers (id, name, gameskill_id, status, data_dir, autostart) VALUES ('on','On','x','running','/x',1)")   //nolint:errcheck
	s.db.Exec("INSERT INTO servers (id, name, gameskill_id, status, data_dir, autostart) VALUES ('off','Off','x','running','/x',0)") //nolint:errcheck
	for _, c := range []struct {
		id   string
		code int
		want bool
	}{
		{"on", 0, true}, {"on", 143, true}, {"on", 1, false}, {"on", 137, false}, {"off", 0, false},
	} {
		if got := s.reviveCandidate(c.id, "cid-"+c.id, c.code); got != c.want {
			t.Errorf("reviveCandidate(%s, exit %d) = %v, want %v", c.id, c.code, got, c.want)
		}
	}
}

// A container that exits 0 without the panel stopping it comes back and keeps
// its status; one the panel stopped stays stopped. Needs YGG_TEST_DOCKER=1.
func TestReconcileRevivesACleanExitButNotAPanelStop(t *testing.T) {
	if os.Getenv("YGG_TEST_DOCKER") == "" {
		t.Skip("YGG_TEST_DOCKER not set")
	}
	dc, err := docker.New("")
	if err != nil {
		t.Skip("no docker")
	}
	s := testServer(t)
	s.docker = dc
	name := "revivetest-" + time.Now().Format("150405")
	if out, err := exec.Command("docker", "run", "-d", "--name", name, "busybox:1.36",
		"sh", "-c", `trap "exit 0" TERM; sleep 300 & wait`).CombinedOutput(); err != nil {
		t.Fatalf("start: %v %s", err, out)
	}
	defer exec.Command("docker", "rm", "-f", "-v", name).Run() //nolint:errcheck
	s.db.Exec("INSERT INTO servers (id, name, gameskill_id, status, data_dir, autostart, container_id) VALUES ('rv','Tunnel','x','running','/x',1,?)", name) //nolint:errcheck

	state := func() (string, string) {
		out, _ := exec.Command("docker", "inspect", "-f", "{{.State.Status}} {{.State.ExitCode}}", name).Output()
		var st string
		s.db.QueryRow("SELECT status FROM servers WHERE id='rv'").Scan(&st) //nolint:errcheck
		return strings.TrimSpace(string(out)), st
	}
	waitExit := func() {
		for i := 0; i < 50; i++ {
			if c, _ := state(); strings.HasPrefix(c, "exited") {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("container did not exit")
	}

	// Stopped from outside the panel: SIGTERM, exit 0, as cloudflared does.
	exec.Command("docker", "kill", "-s", "TERM", name).Run() //nolint:errcheck
	waitExit()
	if c, _ := state(); c != "exited 0" {
		t.Fatalf("container is %q, want exited 0", c)
	}
	s.reconcileStatuses()
	if c, st := state(); !strings.HasPrefix(c, "running") || st != "running" {
		t.Errorf("after an outside stop: container %q, status %q — want both running", c, st)
	}

	// Stopped by the panel: stays stopped.
	if err := s.docker.Stop(t.Context(), name, 10); err != nil {
		t.Fatal(err)
	}
	s.reconcileStatuses()
	if c, st := state(); !strings.HasPrefix(c, "exited") || st != "stopped" {
		t.Errorf("after a panel stop: container %q, status %q — want exited and stopped", c, st)
	}
}
