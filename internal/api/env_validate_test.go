package api

import (
	"strings"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

func intp(n int) *int { return &n }

func testRune() *gameskill.Gameskill {
	return &gameskill.Gameskill{
		ID: "test",
		Variables: []gameskill.Variable{
			{Key: "RAM", Name: "Memory (MB)", Type: "int", Min: intp(512), Max: intp(16384)},
			{Key: "PORT_OFFSET", Type: "int"}, // no bounds
			{Key: "DIFFICULTY", Type: "select", Options: []string{"peaceful", "easy", "hard"}},
			{Key: "PVP", Type: "bool"},
			{Key: "MOTD", Type: "string"},
		},
	}
}

func TestValidateEnvAcceptsGoodValues(t *testing.T) {
	env := map[string]string{
		"RAM": "4096", "PORT_OFFSET": "-3", "DIFFICULTY": "hard", "PVP": "false",
		"MOTD": "anything at all", "UNDECLARED": "passes through",
	}
	if err := validateEnv(testRune(), env); err != nil {
		t.Errorf("rejected a valid env: %v", err)
	}
}

func TestValidateEnvEnforcesBounds(t *testing.T) {
	cases := []struct {
		val, want string
	}{
		{"256", "at least 512"},
		{"999999", "at most 16384"},
		{"banana", "whole number"},
		{"4096.5", "whole number"}, // not an int, however reasonable it looks
	}
	for _, c := range cases {
		err := validateEnv(testRune(), map[string]string{"RAM": c.val})
		if err == nil {
			t.Errorf("RAM=%q was accepted", c.val)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("RAM=%q → %q, want it to mention %q", c.val, err, c.want)
		}
		// The message must name the field, or you can't tell which one to fix.
		if !strings.Contains(err.Error(), "Memory (MB)") {
			t.Errorf("RAM=%q → %q, want the variable's label in the message", c.val, err)
		}
	}
}

func TestValidateEnvEnforcesSelectAndBool(t *testing.T) {
	if err := validateEnv(testRune(), map[string]string{"DIFFICULTY": "nightmare"}); err == nil {
		t.Error("an option outside the declared list was accepted")
	}
	if err := validateEnv(testRune(), map[string]string{"PVP": "yes"}); err == nil {
		t.Error(`PVP="yes" was accepted — it templates straight into a config expecting true/false`)
	}
	if err := validateEnv(testRune(), map[string]string{"PVP": "true"}); err != nil {
		t.Errorf(`PVP="true" rejected: %v`, err)
	}
}

// An unbounded int still has to be an int, but takes any value.
func TestValidateEnvUnboundedInt(t *testing.T) {
	if err := validateEnv(testRune(), map[string]string{"PORT_OFFSET": "999999999"}); err != nil {
		t.Errorf("unbounded int rejected: %v", err)
	}
	if err := validateEnv(testRune(), map[string]string{"PORT_OFFSET": "x"}); err == nil {
		t.Error("a non-numeric unbounded int was accepted")
	}
}

// Blank means "use the rune's default" — the create form submits empty strings
// for untouched optional fields, so rejecting those makes the dialog unusable.
func TestValidateEnvAllowsBlank(t *testing.T) {
	env := map[string]string{"RAM": "", "DIFFICULTY": "", "PVP": ""}
	if err := validateEnv(testRune(), env); err != nil {
		t.Errorf("a blank value was rejected: %v", err)
	}
}

// Env has sources beyond the rune's variables — port injection, HOME,
// SERVER_NAME — so an undeclared key is not an error.
func TestValidateEnvIgnoresUndeclaredKeys(t *testing.T) {
	if err := validateEnv(testRune(), map[string]string{"GAME_PORT": "25565", "HOME": "/data"}); err != nil {
		t.Errorf("undeclared keys rejected: %v", err)
	}
}

func TestValidateEnvHandlesNilRune(t *testing.T) {
	if err := validateEnv(nil, map[string]string{"RAM": "banana"}); err != nil {
		t.Errorf("a nil rune should be a no-op, got %v", err)
	}
}
