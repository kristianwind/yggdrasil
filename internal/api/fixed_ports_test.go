package api

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kristianwind/yggdrasil/internal/docker"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// A port that is taken has to be explained by what took it, or "port 25 is in
// use" sends the admin hunting with ss and docker ps for what the panel knew.
func TestPortHolderNamesWhatHasThePort(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	s.db.Exec("INSERT INTO servers (id, name, gameskill_id, status, data_dir) VALUES ('srv-mail','Mail','stalwart','running','/x')") //nolint:errcheck
	s.db.Exec("INSERT INTO port_allocations (port, server_id, name) VALUES (40025,'srv-mail','smtp')")                               //nolint:errcheck
	if got := s.portHolder(ctx, 40025); got != `the server "Mail"` {
		t.Errorf("portHolder(allocated) = %q", got)
	}
	err := s.validatePortChoice(ctx, 40025, nil)
	if err == nil || !strings.Contains(err.Error(), `the server "Mail"`) {
		t.Errorf("validatePortChoice = %v, want it to name the server", err)
	}

	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	busy := ln.Addr().(*net.TCPAddr).Port
	if got := s.portHolder(ctx, busy); got != "another program on this host" {
		t.Errorf("portHolder(bound elsewhere) = %q", got)
	}
}

func TestFixedPortErrSaysWhatToDo(t *testing.T) {
	err := fixedPortErr(gameskill.Port{Name: "smtp", Default: 25}, errors.New(`already in use by the server "Old mail"`))
	for _, want := range []string{`"smtp"`, "exactly 25", `"Old mail"`, "under Ports"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

// pauseServer freezes every running container of the server, marks it so the
// watchdog leaves it alone, and the returned function thaws all of them.
// Needs Docker and YGG_TEST_DOCKER=1.
func TestPauseServerFreezesAndThawsTheStack(t *testing.T) {
	if os.Getenv("YGG_TEST_DOCKER") == "" {
		t.Skip("YGG_TEST_DOCKER not set")
	}
	dc, err := docker.New("")
	if err != nil {
		t.Skip("no docker")
	}
	s := testServer(t)
	s.docker = dc
	id := "pausetest-" + time.Now().Format("150405")
	var names []string
	for _, role := range []string{"app", "db"} {
		n := id + "-" + role
		names = append(names, n)
		if out, err := exec.Command("docker", "run", "-d", "--rm", "--name", n, "--label", "yggdrasil.server_id="+id,
			"busybox:1.36", "sleep", "300").CombinedOutput(); err != nil {
			t.Fatalf("start %s: %v %s", n, err, out)
		}
	}
	defer exec.Command("docker", append([]string{"rm", "-f", "-v"}, names...)...).Run() //nolint:errcheck

	state := func(n string) string {
		out, _ := exec.Command("docker", "inspect", "-f", "{{.State.Status}}", n).Output()
		return strings.TrimSpace(string(out))
	}
	thaw := s.pauseServer(context.Background(), id)
	for _, n := range names {
		if st := state(n); st != "paused" {
			t.Errorf("%s is %q while backing up, want paused", n, st)
		}
	}
	if _, ok := backupPaused.Load(id); !ok {
		t.Error("server not marked as paused for the watchdog")
	}
	thaw()
	for _, n := range names {
		if st := state(n); st != "running" {
			t.Errorf("%s is %q after the backup, want running", n, st)
		}
	}
	if _, ok := backupPaused.Load(id); ok {
		t.Error("server still marked paused after thaw")
	}
}
