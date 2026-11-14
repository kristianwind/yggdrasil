package api

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

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

	// A shape the rune declares. This is the last chance to say something useful:
	// past here the value is the container's problem, and what comes back is
	// whatever that program prints before exiting — "invalid pihole format",
	// naming neither the field nor what was wrong with it.
	//
	// Compiled rather than cached because validate() already proved it compiles
	// at upload, and this runs once per save, not per request.
	if v.Pattern != "" {
		re, err := regexp.Compile(v.Pattern)
		if err != nil {
			return nil // a rune that cannot compile its own check does not get to block a save
		}
		if !re.MatchString(raw) {
			if v.Hint != "" {
				return fmt.Errorf("%s", v.Hint)
			}
			return fmt.Errorf("does not have the expected format")
		}
	}
	return nil
}

// --- what a non-admin may put in a variable -------------------------------

// shellCtx is how a {{VAR}} sits in a piece of shell: which characters would
// let its value stop being a value.
type shellCtx int

const (
	ctxDouble shellCtx = 1 << iota // inside "..."
	ctxSingle                      // inside '...'
	ctxBare                        // unquoted, or an image name / uid that is not shell at all
)

// templateRe finds the placeholders ApplyTemplate fills in.
var templateRe = regexp.MustCompile(`{{([A-Za-z0-9_]+)}}`)

// scanShell records, for every {{VAR}} in a shell script, the quoting it is
// found in. It knows single quotes, double quotes, backslashes and comments —
// enough for the scripts runes are actually written in. Where it is wrong the
// rules in delegateValueError overlap, so one misread quote costs a refused
// value rather than an accepted one.
func scanShell(script string, into map[string]shellCtx) {
	locs := templateRe.FindAllStringSubmatchIndex(script, -1)
	next := 0
	var quote byte
	for i := 0; i < len(script) && next < len(locs); i++ {
		if i == locs[next][0] {
			key := script[locs[next][2]:locs[next][3]]
			switch quote {
			case '"':
				into[key] |= ctxDouble
			case '\'':
				into[key] |= ctxSingle
			default:
				into[key] |= ctxBare
			}
			i = locs[next][1] - 1
			next++
			continue
		}
		c := script[i]
		switch quote {
		case 0:
			switch {
			case c == '\'' || c == '"':
				quote = c
			case c == '\\':
				i++
			case c == '#' && (i == 0 || strings.ContainsRune(" \t\n;", rune(script[i-1]))):
				for i < len(script) && script[i] != '\n' {
					// A placeholder in a comment is still substituted, and a
					// newline in its value would end the comment: bare.
					if next < len(locs) && i == locs[next][0] {
						into[script[locs[next][2]:locs[next][3]]] |= ctxBare
						next++
					}
					i++
				}
			}
		case '\'':
			if c == '\'' {
				quote = 0
			}
		case '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				quote = 0
			}
		}
	}
}

// codeContexts answers: which of this rune's variables end up inside something
// that is executed or that chooses what is executed, and how.
//
// A rune's scripts are templates. `VER="{{MC_VERSION}}"` is filled in by plain
// text substitution and the result is handed to /bin/sh — for install and update,
// as root. The value is therefore part of the program, and whoever may set the
// variable may write the program.
func codeContexts(gs *gameskill.Gameskill) map[string]shellCtx {
	out := map[string]shellCtx{}
	if gs == nil {
		return out
	}
	scripts := []string{gs.Startup.Command}
	bare := []string{gs.Docker.Image, gs.Docker.User}
	if gs.Install != nil {
		scripts = append(scripts, gs.Install.Script)
		bare = append(bare, gs.Install.Image)
	}
	if gs.Update != nil {
		scripts = append(scripts, gs.Update.Script)
		bare = append(bare, gs.Update.Image)
	}
	if gs.Import != nil {
		for _, st := range gs.Import.Steps {
			scripts = append(scripts, st.Script)
			if st.DBImport != nil {
				scripts = append(scripts, st.DBImport.Command)
				bare = append(bare, st.DBImport.Image)
			}
		}
	}
	for _, sc := range scripts {
		scanShell(sc, out)
	}
	// Not shell, but the same question: an image name or a uid built from a
	// variable is chosen by whoever sets it.
	for _, svc := range gs.Services {
		bare = append(bare, svc.Image, svc.Enabled)
	}
	for _, b := range bare {
		for _, m := range templateRe.FindAllStringSubmatch(b, -1) {
			out[m[1]] |= ctxBare
		}
	}
	return out
}

var bareSafeRe = regexp.MustCompile(`^[A-Za-z0-9_.,:@%+=/-]*$`)

// delegateValueError says why a value cannot go where the rune puts it, or "".
func delegateValueError(ctx shellCtx, raw string) string {
	if ctx == 0 || raw == "" {
		return ""
	}
	if strings.ContainsAny(raw, "\n\r\x00") {
		return "cannot contain a line break"
	}
	// {{ could name another variable and be substituted a second time.
	if strings.Contains(raw, "{{") {
		return "cannot contain {{"
	}
	if ctx&ctxBare != 0 {
		if !bareSafeRe.MatchString(raw) || strings.HasPrefix(raw, "-") {
			return "can only contain letters, digits and _ . , : @ % + = / -"
		}
		return ""
	}
	// Quoted. Forbid what ends or escapes either kind of quote unless the scan is
	// sure which kind it is — and $ ` \ whenever a double quote is involved.
	if strings.ContainsAny(raw, "\"`$\\") {
		return "cannot contain \" ` $ or \\"
	}
	if ctx&ctxSingle != 0 && strings.Contains(raw, "'") {
		return "cannot contain '"
	}
	return ""
}

// guardDelegateEnv is validateEnv's other half, for a caller who is not an
// admin. validateEnv asks whether a value is what the rune declared; this asks
// whether setting it is a way to run something.
//
// server.control lets a delegate edit a server's variables and reinstall it.
// Together those were a root shell in the install container: set MC_VERSION to
// `x"; <anything>; #` and press Reinstall. An admin is not checked — an admin
// can upload a rune, which is the same power stated openly.
//
// Two rules. A key the rune does not declare is refused: nothing a delegate
// should be setting lives outside the form, and an undeclared key is otherwise
// free text into every template that happens to mention it. And a declared
// variable that a script, an image name or a uid is built from must fit the
// quoting it is used in. int, bool and select are already closed sets.
func guardDelegateEnv(gs *gameskill.Gameskill, env map[string]string) error {
	if gs == nil {
		if len(env) > 0 {
			return fmt.Errorf("this server's rune could not be read, so only an admin can change its settings")
		}
		return nil
	}
	declared := map[string]gameskill.Variable{}
	for _, v := range gs.Variables {
		declared[v.Key] = v
	}
	ctxs := codeContexts(gs)
	for k, raw := range env {
		v, ok := declared[k]
		if k == "SERVER_NAME" && !ok {
			// Not a variable: the panel injects the server's name under this key,
			// and runes template it into start commands like any other value.
			if why := delegateValueError(ctxs[k], raw); why != "" {
				return fmt.Errorf("the server's name %s, because this server's start or install script uses it. An admin can set it to anything", why)
			}
			continue
		}
		if !ok {
			return fmt.Errorf("%s is not one of this server's settings — only an admin can set it", k)
		}
		// validateEnv has already held these to a number, true/false, or one of
		// the rune's own options.
		if v.Type == "int" || v.Type == "bool" || (v.Type == "select" && len(v.Options) > 0) {
			continue
		}
		if why := delegateValueError(ctxs[k], raw); why != "" {
			return fmt.Errorf("%s %s, because it is used in this server's start or install script. An admin can set it to anything", label(v), why)
		}
	}
	return nil
}
