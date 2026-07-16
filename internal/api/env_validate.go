package api

import (
	"fmt"
	"strconv"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// validateEnv checks submitted variable values against what the rune declares.
//
// Until now nothing did. A rune could say `type: int, min: 512, max: 16384` for
// RAM and the panel would accept "banana" — the value went straight into the
// container's environment, and you found out when the server failed to boot with
// something unrelated-looking. The type, the options and the bounds were all
// documentation rather than rules.
//
// Only keys present in env AND declared by the rune are checked. Unknown keys
// pass: runtime env has other sources (port injection, HOME, SERVER_NAME) and a
// rune's variable list was never meant to be the complete set.
//
// Callers choose what to hand it, and the two paths differ on purpose:
//
//   - create passes the merged env — the rune's defaults with the operator's
//     values over the top. That also catches a rune whose own default contradicts
//     its declared type, which is worth a clear error at create rather than a
//     confusing one at boot. Verified against all 38 shipped runes: none fail.
//   - update passes only the fields being changed. Validating the merged result
//     there would strand existing servers: a rune that tightens its bounds later
//     would make every edit fail, including edits to unrelated fields. The next
//     time you touch a value is when it has to be right.
func validateEnv(gs *gameskill.Gameskill, env map[string]string) error {
	if gs == nil || len(env) == 0 {
		return nil
	}
	for _, v := range gs.Variables {
		raw, supplied := env[v.Key]
		if !supplied {
			continue
		}
		if err := validateVar(v, raw); err != nil {
			return fmt.Errorf("%s: %w", label(v), err)
		}
	}
	return nil
}

func label(v gameskill.Variable) string {
	if v.Name != "" {
		return v.Name
	}
	return v.Key
}

func validateVar(v gameskill.Variable, raw string) error {
	// An empty value means "use the rune's default" — the form submits blanks for
	// untouched optional fields, and rejecting those would make the dialog
	// unusable. `required` is the UI's affair; it has never been enforced here and
	// making it so is a separate decision.
	if raw == "" {
		return nil
	}

	switch v.Type {
	case "int":
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("must be a whole number, got %q", raw)
		}
		if v.Min != nil && n < *v.Min {
			return fmt.Errorf("must be at least %d, got %d", *v.Min, n)
		}
		if v.Max != nil && n > *v.Max {
			return fmt.Errorf("must be at most %d, got %d", *v.Max, n)
		}
	case "select":
		if len(v.Options) == 0 {
			return nil // validate() rejects this at upload; nothing to check against
		}
		for _, opt := range v.Options {
			if raw == opt {
				return nil
			}
		}
		return fmt.Errorf("must be one of %v, got %q", v.Options, raw)
	case "bool":
		// The startup command templates this straight in, so a typo becomes a
		// literal "yes" in a config file that expects true/false.
		if raw != "true" && raw != "false" {
			return fmt.Errorf("must be true or false, got %q", raw)
		}
	}
	return nil
}
