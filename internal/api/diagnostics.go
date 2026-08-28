package api

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Diagnostics for a bug report.
//
// Twenty-two installs and an issue tracker that had never been used once: the
// reporting channel existed, but nothing inside the panel pointed at it, and an
// admin who hit a bug had no way to know where to say so. This is the door.
//
// The bundle is built from an ALLOWLIST, never a dump. That distinction is the
// whole design and it must survive future edits: every field below is here
// because someone decided it is safe to hand a stranger, not because it happened
// to be in a struct. A dump would eventually pick up a server name, a domain, a
// backup target's credentials or a rune variable, and the admin pasting it into a
// public issue would never know.
//
// Deliberately absent, each for its own reason:
//
//   - Server names, domains and file paths — usually a person's real name, their
//     company, or their home directory layout.
//   - Rune variable values — some are secrets, and the ones that aren't marked
//     secret: true are stored in plaintext precisely because nobody flagged them.
//   - IP addresses and the hostname — locates the person.
//   - The beacon's instance_id — including it would let a report be joined to
//     beacon records, turning an anonymous count into an identified install. The
//     beacon is anonymous by promise; a bug report must not quietly undo that.
//   - Custom rune names, which are counted but never listed: a rune someone wrote
//     themselves can be named after their employer or their game community.
//
// Nothing here is transmitted by the panel. The bundle is rendered, shown to the
// admin in full, and it travels only if they copy it or press the button that
// opens a prefilled issue form on github.com. The panel makes no outbound request.
const issueTemplateURL = "https://github.com/kristianwind/yggdrasil/issues/new?template=bug_report.yml"

// osPrettyName reads PRETTY_NAME from /etc/os-release. Returns "" on anything
// unexpected — a missing file, a non-Linux host — and the caller degrades to the
// bare architecture rather than guessing.
func osPrettyName() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "PRETTY_NAME=") {
			continue
		}
		return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
	}
	return ""
}

func gib(b uint64) string {
	if b == 0 {
		return "unknown"
	}
	return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
}

// handleDiagnostics renders the bundle. Admin-only: it reports install-wide
// counts, which is not a delegate's business, and only an admin can act on a bug
// report anyway.
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	host := osPrettyName()
	if host == "" {
		host = "unknown OS"
	}
	host = fmt.Sprintf("%s, %s", host, runtime.GOARCH)

	// Bounded: a wedged Docker daemon accepts the connection and never answers,
	// and the version line is the least important thing here. Without this the
	// whole endpoint hangs on exactly the broken host most likely to be filing
	// the bug report.
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	dockerVer, dockerAPI := s.docker.Version(dctx)
	dockerLine := "unreachable"
	if dockerVer != "" {
		dockerLine = fmt.Sprintf("%s (API %s)", dockerVer, dockerAPI)
	}

	var servers, running, users int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM servers").Scan(&servers)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM servers WHERE status='running'").Scan(&running)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&users)

	// Builtin runes are listed by ID, not display name, for two reasons: the ID is
	// the catalogue slug the docs and the issue template already use
	// ("minecraft-java"), and unlike the name it isn't free text an admin may have
	// edited into something personal. Custom runes are counted only — see above.
	var builtins []string
	custom := 0
	if rows, err := s.db.QueryContext(ctx, "SELECT id, version, builtin FROM gameskills"); err == nil {
		defer rows.Close()
		for rows.Next() {
			var id string
			var version, builtin int
			if rows.Scan(&id, &version, &builtin) != nil {
				continue
			}
			if builtin == 1 {
				builtins = append(builtins, fmt.Sprintf("%s v%d", id, version))
			} else {
				custom++
			}
		}
	}
	sort.Strings(builtins)
	runeLine := strings.Join(builtins, ", ")
	if runeLine == "" {
		runeLine = "none"
	}
	if custom > 0 {
		runeLine += fmt.Sprintf(" (+%d custom, not listed)", custom)
	}

	memTotal, _ := hostMem()
	free, total := diskUsage(filepath.Dir(s.cfg.Database.Path))

	var b strings.Builder
	fmt.Fprintf(&b, "Panel     %s\n", s.version)
	fmt.Fprintf(&b, "Host      %s, %d CPU\n", host, runtime.NumCPU())
	fmt.Fprintf(&b, "Docker    %s\n", dockerLine)
	fmt.Fprintf(&b, "Go        %s\n", runtime.Version())
	fmt.Fprintf(&b, "Memory    %s total\n", gib(memTotal))
	fmt.Fprintf(&b, "Disk      %s free of %s\n", gib(free), gib(total))
	fmt.Fprintf(&b, "Servers   %d (%d running)\n", servers, running)
	fmt.Fprintf(&b, "Users     %d\n", users)
	fmt.Fprintf(&b, "Runes     %s\n", runeLine)

	jsonOK(w, map[string]any{
		"report":  b.String(),
		"version": s.version,
		"host":    host,
		"url":     issueTemplateURL,
	})
}
