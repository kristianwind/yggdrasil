package gameskill

import "testing"

const validYAML = `
gameskill:
  id: test-game
  name: "Test Game"
  category: "Testing"
  docker:
    image: "alpine:{{TAG}}"
  variables:
    - key: TAG
      name: "Image tag"
      type: select
      options: ["latest", "edge"]
      default: latest
    - key: PORT
      name: "Port"
      type: int
      default: 8080
  startup:
    command: "./run --port {{PORT}}"
  ports:
    - { name: game, default: 8080, protocol: tcp }
`

func TestParseValid(t *testing.T) {
	gs, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gs.ID != "test-game" {
		t.Errorf("id = %q, want test-game", gs.ID)
	}
	if len(gs.Variables) != 2 {
		t.Errorf("got %d variables, want 2", len(gs.Variables))
	}
	if gs.Variables[0].Type != "select" || len(gs.Variables[0].Options) != 2 {
		t.Errorf("select variable not parsed correctly: %+v", gs.Variables[0])
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"missing id": `
gameskill:
  name: "X"
  docker: { image: "alpine" }
  startup: { command: "./x" }`,
		"missing image": `
gameskill:
  id: x
  name: "X"
  startup: { command: "./x" }`,
		"missing startup command": `
gameskill:
  id: x
  name: "X"
  docker: { image: "alpine" }`,
		"unknown variable type": `
gameskill:
  id: x
  name: "X"
  docker: { image: "alpine" }
  startup: { command: "./x" }
  variables:
    - { key: K, name: K, type: float }`,
		"select without options": `
gameskill:
  id: x
  name: "X"
  docker: { image: "alpine" }
  startup: { command: "./x" }
  variables:
    - { key: K, name: K, type: select }`,
		"bad port protocol": `
gameskill:
  id: x
  name: "X"
  docker: { image: "alpine" }
  startup: { command: "./x" }
  ports:
    - { name: game, default: 80, protocol: sctp }`,
	}
	for name, yaml := range cases {
		if _, err := Parse([]byte(yaml)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestApplyTemplate(t *testing.T) {
	env := map[string]string{"TAG": "edge", "PORT": "9000"}
	got := ApplyTemplate("alpine:{{TAG}} on {{PORT}}", env)
	want := "alpine:edge on 9000"
	if got != want {
		t.Errorf("ApplyTemplate = %q, want %q", got, want)
	}
}

func TestDefaultEnv(t *testing.T) {
	gs, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	env := DefaultEnv(gs)
	if env["TAG"] != "latest" {
		t.Errorf("default TAG = %q, want latest", env["TAG"])
	}
	if env["PORT"] != "8080" {
		t.Errorf("default PORT = %q, want 8080", env["PORT"])
	}
}
