package api

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Host OS update status — read-only.
//
// The panel reports what the box has pending; it does not apply anything. That
// split is deliberate:
//
//   - Reading needs no privileges. The panel runs as an unprivileged user under
//     NoNewPrivileges + ProtectSystem, and `apt list`/`apt-check` are happy as
//     any user, so this costs nothing and can't break a host.
//   - Applying would. `apt upgrade` restarts docker.service when the Docker
//     packages move, which kills every running game server without warning — and
//     unattended-upgrades already does the applying properly. If the panel ever
//     does it, it should be through the same request-file → root path-unit dance
//     the self-updater uses, at a quiet hour, and saying out loud that servers
//     will bounce.
//
// It exists because SECURITY.md tells operators that the fix for the open Docker
// advisories is keeping Docker updated on the host, and until now nothing in the
// panel would tell them whether they had.
type osUpdates struct {
	Supported bool `json:"supported"` // apt is present; false on anything else
	Total     int  `json:"total"`
	// Security is nil when we can't tell, which is not the same as zero and must
	// not be rendered as it. Only apt-check knows; a bare `apt list --upgradable`
	// doesn't, because Ubuntu publishes security updates into both -security and
	// -updates and shows whichever the policy picked.
	Security       *int     `json:"security,omitempty"`
	RebootRequired bool     `json:"reboot_required"`
	RebootPkgs     []string `json:"reboot_pkgs,omitempty"`
	// CacheAgeHours is how long since apt last refreshed its package lists. A big
	// number means the counts are stale, not that the box is up to date — worth
	// showing, because "0 updates" from a month-old cache is a lie by omission.
	CacheAgeHours *float64 `json:"cache_age_hours,omitempty"`
	CheckedAt     string   `json:"checked_at"`
	Source        string   `json:"source,omitempty"` // apt-check | apt-list
	Note          string   `json:"note,omitempty"`
}

const (
	aptCheckPath  = "/usr/lib/update-notifier/apt-check" // Ubuntu's own; what the MOTD prints
	rebootFlag    = "/var/run/reboot-required"
	rebootPkgs    = "/var/run/reboot-required.pkgs"
	aptStamp      = "/var/lib/apt/periodic/update-success-stamp"
	osUpdateTTL   = 15 * time.Minute
	osUpdateLimit = 20 * time.Second
)

type osUpdateCache struct {
	mu   sync.Mutex
	val  *osUpdates
	when time.Time
}

// handleOSUpdates reports the host's pending updates. Admin-only: it's host
// state, like /api/system/info, and none of a delegate's business.
func (s *Server) handleOSUpdates(w http.ResponseWriter, r *http.Request) {
	s.osUpd.mu.Lock()
	if s.osUpd.val != nil && time.Since(s.osUpd.when) < osUpdateTTL {
		cached := *s.osUpd.val
		s.osUpd.mu.Unlock()
		jsonOK(w, cached)
		return
	}
	s.osUpd.mu.Unlock()

	// Shelling out is new here — everything else the panel runs goes through
	// Docker. Keep it tight: a fixed argv (no shell, and no user input to inject
	// anyway) under a hard deadline, so a wedged apt can't hold a request open.
	ctx, cancel := context.WithTimeout(r.Context(), osUpdateLimit)
	defer cancel()
	res := readOSUpdates(ctx)

	s.osUpd.mu.Lock()
	s.osUpd.val, s.osUpd.when = &res, time.Now()
	s.osUpd.mu.Unlock()
	jsonOK(w, res)
}

func readOSUpdates(ctx context.Context) osUpdates {
	out := osUpdates{CheckedAt: time.Now().UTC().Format(time.RFC3339)}

	if _, err := exec.LookPath("apt"); err != nil {
		out.Note = "This host doesn't use apt, so the panel can't read its update status."
		return out
	}
	out.Supported = true

	if n, sec, err := aptCheck(ctx); err == nil {
		out.Total, out.Security, out.Source = n, &sec, "apt-check"
	} else if n, err := aptListUpgradable(ctx); err == nil {
		out.Total, out.Source = n, "apt-list"
		out.Note = "Install update-notifier-common for a security-update count."
	} else {
		out.Supported = false
		out.Note = "Could not read the update status from apt."
		return out
	}

	// Present on Debian/Ubuntu once something wants a reboot — usually the kernel.
	// World-readable, so no privileges needed to notice.
	if _, err := os.Stat(rebootFlag); err == nil {
		out.RebootRequired = true
		if b, err := os.ReadFile(rebootPkgs); err == nil {
			for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				if l = strings.TrimSpace(l); l != "" {
					out.RebootPkgs = append(out.RebootPkgs, l)
				}
			}
		}
	}

	if fi, err := os.Stat(aptStamp); err == nil {
		h := time.Since(fi.ModTime()).Hours()
		out.CacheAgeHours = &h
	}
	return out
}

// aptCheck asks Ubuntu's own checker, which prints "total;security" on stderr.
// It's the number the operator already sees in the MOTD at login, so the panel
// agreeing with it is worth more than the panel counting for itself.
func aptCheck(ctx context.Context) (total, security int, err error) {
	if _, err := os.Stat(aptCheckPath); err != nil {
		return 0, 0, err // Debian, or update-notifier-common not installed
	}
	cmd := exec.CommandContext(ctx, aptCheckPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, 0, err
	}
	parts := strings.SplitN(strings.TrimSpace(stderr.String()), ";", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected apt-check output %q", stderr.String())
	}
	total, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	security, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return total, security, nil
}

// aptListUpgradable is the fallback for hosts without apt-check (Debian). It
// counts lines only: the suite in this output can't be trusted to identify a
// security update, since Ubuntu publishes those to both -security and -updates
// and lists whichever the policy chose. A wrong "0 security" is worse than none.
func aptListUpgradable(ctx context.Context) (int, error) {
	cmd := exec.CommandContext(ctx, "apt", "list", "--upgradable")
	// apt prints "WARNING: apt does not have a stable CLI interface" to stderr;
	// that's expected, and we only read stdout.
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return 0, err
	}
	n := 0
	sc := bufio.NewScanner(&stdout)
	for sc.Scan() {
		if strings.Contains(sc.Text(), "upgradable from:") {
			n++
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return n, nil
}
