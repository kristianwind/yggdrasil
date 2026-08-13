package gameskill

import (
	"os"
	"strings"
	"testing"
)

// Players wanting hardcore were typing it into server.properties as a
// difficulty. It is not one — Minecraft's difficulty is peaceful/easy/normal/
// hard, and hardcore is a separate boolean. The rune has to offer it, or the
// only way to ask for it is a value the game rejects.
func TestMinecraftJavaOffersHardcore(t *testing.T) {
	b, err := os.ReadFile("../../builtin-runes/minecraft-java.yaml")
	if err != nil {
		t.Fatal(err)
	}
	gs, err := Parse(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var difficulty, hardcore *Variable
	for i := range gs.Variables {
		switch gs.Variables[i].Key {
		case "DIFFICULTY":
			difficulty = &gs.Variables[i]
		case "HARDCORE":
			hardcore = &gs.Variables[i]
		}
	}

	if hardcore == nil {
		t.Fatal("no HARDCORE variable — hardcore can only be asked for as an invalid difficulty")
	}
	if hardcore.Type != "bool" {
		t.Errorf("HARDCORE type = %q, want bool", hardcore.Type)
	}

	if difficulty == nil {
		t.Fatal("no DIFFICULTY variable")
	}
	// The four Minecraft accepts, and specifically not "hardcore".
	for _, opt := range difficulty.Options {
		if opt == "hardcore" {
			t.Error("hardcore is offered as a difficulty; Minecraft does not accept it there")
		}
	}
	if len(difficulty.Options) != 4 {
		t.Errorf("difficulty options = %v, want the four Minecraft accepts", difficulty.Options)
	}
}

// The setting is worthless if the startup command doesn't write it: install
// runs once, so anything only stamped there looks applied while the server
// keeps the old value — the same trap difficulty and the whitelist already
// avoid by being re-stamped.
func TestMinecraftJavaReStampsHardcore(t *testing.T) {
	b, _ := os.ReadFile("../../builtin-runes/minecraft-java.yaml")
	gs, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	cmd := gs.Startup.Command
	for _, want := range []string{"setprop hardcore {{HARDCORE}}", "setprop difficulty {{DIFFICULTY}}"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("startup command does not re-stamp: %q missing", want)
		}
	}
}
