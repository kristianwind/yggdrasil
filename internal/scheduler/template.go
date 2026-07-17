// Package scheduler renders message templates and defines the schedule action
// model. The cron runner itself lives in the api package, which has access to
// Docker, RCON and backups; this package holds the pure, testable pieces.
package scheduler

import (
	"fmt"
	"regexp"
	"strings"
)

// Render substitutes {{key}} placeholders in body with values from vars.
// Unknown placeholders are left untouched so mistakes are visible, not silent.
func Render(body string, vars map[string]string) string {
	out := body
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

var placeholderRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

// Placeholders lists the {{key}} names in body, in order, without duplicates.
// Callers use it two ways: to ask an admin for the values a template needs, and
// to check a rendered message for placeholders Render couldn't fill — the point
// of leaving those untouched is that somebody looks.
func Placeholders(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range placeholderRe.FindAllStringSubmatch(body, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// DefaultTemplate is a built-in in-game message shipped with Yggdrasil.
type DefaultTemplate struct {
	Name string
	Body string
}

// DefaultTemplates are seeded on first boot and editable afterwards. The body is
// the full console/RCON command so it works across game types (the admin tunes
// it per game if needed).
var DefaultTemplates = []DefaultTemplate{
	{"Restart warning", "say Server restarting in {{minutes}} minutes. Please find a safe spot."},
	{"Restart countdown", "say Restarting in {{seconds}} seconds!"},
	{"Backup warning", "say Backing up {{server_name}} — you may notice a brief lag spike."},
	{"Maintenance notice", "say {{server_name}} is going down for maintenance shortly."},
	{"Update warning", "say An update is ready. {{server_name}} will restart in {{minutes}} minutes."},
}

// Action is a schedule action type.
type Action string

const (
	ActionBackup  Action = "backup"
	ActionRestart Action = "restart"
	ActionStart   Action = "start"
	ActionStop    Action = "stop"
	ActionCommand Action = "command" // raw RCON/console command
	ActionMessage Action = "message" // rendered template sent to players
	ActionUpdate  Action = "update"  // re-run install/SteamCMD update
	ActionWipe    Action = "wipe"    // reset world/persistence (rune-defined)
)

// AllActions lists every known action. It is the single source of truth for
// what a schedule may do: ValidAction is derived from it, and the API's
// per-action permission table is tested for exhaustiveness against it, so a new
// action can't be added without deciding what permission it requires.
var AllActions = []Action{
	ActionBackup, ActionRestart, ActionStart, ActionStop,
	ActionCommand, ActionMessage, ActionUpdate, ActionWipe,
}

// ValidAction reports whether a is a known action.
func ValidAction(a Action) bool {
	for _, known := range AllActions {
		if a == known {
			return true
		}
	}
	return false
}

// ValidateCron does a light sanity check on a cron expression (5 or 6 fields).
// The full parse happens in robfig/cron when the schedule is registered.
func ValidateCron(expr string) error {
	n := len(strings.Fields(expr))
	if n != 5 && n != 6 {
		return fmt.Errorf("cron expression must have 5 or 6 fields, got %d", n)
	}
	return nil
}
