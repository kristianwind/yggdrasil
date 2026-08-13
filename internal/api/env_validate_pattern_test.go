package api

import (
	"os"
	"strings"
	"testing"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// The exact mistake this was built for: nebula-sync's fields take
// "address|password" in one string, and the first person to fill them in
// pasted the password alone. The container exited two seconds later with
// "invalid pihole format", naming neither the field nor what was wrong.
func TestPatternCatchesAPasswordWithoutItsAddress(t *testing.T) {
	b, err := os.ReadFile("../../community-runes/apps/nebula-sync.yaml")
	if err != nil {
		t.Fatal(err)
	}
	gs, err := gameskill.Parse(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	err = validateEnv(gs, map[string]string{"PRIMARY": "somepassword"})
	if err == nil {
		t.Fatal("a bare password was accepted; the rune's pattern is not being enforced")
	}
	if !strings.Contains(err.Error(), "pipe") {
		t.Errorf("error was %q — it should tell the user what the value must look like", err)
	}

	if err := validateEnv(gs, map[string]string{"PRIMARY": "http://192.168.1.3|somepassword"}); err != nil {
		t.Errorf("a correct value was rejected: %v", err)
	}
}

func TestPatternAcceptsSeveralReplicas(t *testing.T) {
	b, _ := os.ReadFile("../../community-runes/apps/nebula-sync.yaml")
	gs, _ := gameskill.Parse(b)

	ok := []string{
		"http://192.168.1.4|pw",
		"http://a.lan|pw,http://b.lan|pw",
		"https://a.lan|pw, https://b.lan|pw",
	}
	for _, v := range ok {
		if err := validateEnv(gs, map[string]string{"REPLICAS": v}); err != nil {
			t.Errorf("rejected a valid replica list %q: %v", v, err)
		}
	}
	bad := []string{"pw", "192.168.1.4|pw", "http://a.lan"}
	for _, v := range bad {
		if err := validateEnv(gs, map[string]string{"REPLICAS": v}); err == nil {
			t.Errorf("accepted an invalid replica list %q", v)
		}
	}
}

// An untouched optional field submits blank, and blanking a field means "use the
// rune's default". A pattern must not turn that into an error.
func TestPatternSkipsEmptyValues(t *testing.T) {
	v := gameskill.Variable{Key: "X", Type: "string", Pattern: `^\d+$`}
	if err := validateVar(v, ""); err != nil {
		t.Errorf("an empty value was rejected: %v", err)
	}
}

// A rune whose pattern does not compile must not be able to block saves. It is
// rejected at upload; if one ever gets through, failing open is the safer end.
func TestPatternFailsOpenOnABadRegex(t *testing.T) {
	v := gameskill.Variable{Key: "X", Type: "string", Pattern: "([unclosed"}
	if err := validateVar(v, "anything"); err != nil {
		t.Errorf("an uncompilable pattern blocked a save: %v", err)
	}
}

// And the parser rejects it up front, so it never reaches a server form.
func TestBadPatternRejectedAtParse(t *testing.T) {
	y := []byte(`gameskill:
  id: broken
  name: "Broken"
  version: 1
  docker: { image: "busybox:latest", keep_entrypoint: true }
  variables:
    - { key: X, name: "X", type: string, pattern: "([unclosed" }
  startup: { command: "", done_regex: "x" }
`)
	if _, err := gameskill.Parse(y); err == nil {
		t.Fatal("a rune with an uncompilable pattern was accepted")
	}
}
