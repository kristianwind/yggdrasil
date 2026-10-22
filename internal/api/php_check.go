package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// PHP that no longer gets security fixes.
//
// Most of what this panel hosts that is reachable from the internet is PHP:
// WordPress, the PHP-site rune, Nextcloud, phpMyAdmin, Adminer. Plus whatever PHP
// was installed on the host itself before the panel arrived, which on a box that
// used to serve websites directly is usually still there and still running
// php-fpm. A PHP branch past its end of life gets no fixes at all, so every
// vulnerability found in it afterwards stays open forever. Nothing in the panel
// said so: the site works, it looks fine, and the version is only visible if you
// go looking for it.
//
// So the panel looks. Every six hours it asks the host's own PHP binaries and
// every running server which PHP they are, and compares the branch with PHP's
// published support dates. A branch past its end of life is an emergency: a
// notification, an incident in the activity feed, and a red banner at the top
// of every page until it is gone. A branch with less than phpEndingSoon left
// gets the same, in orange, while there is still time to plan the move.
//
// It is read-only throughout: it runs `php -v` and reads package metadata, and it
// never installs, upgrades or restarts anything. Moving a site to a new PHP
// breaks plugins often enough that it has to be somebody's decision.

// phpSecurityEnd is the last day each PHP branch gets security fixes, from
// php.net/supported-versions.php and php.net/eol.php (checked 2026-10-22).
//
// A branch newer than the newest one here counts as supported. That is the
// direction this table goes stale in, and it is the safe one: the table only
// has to be updated for the warning to start, never for it to stop.
var phpSecurityEnd = map[string]string{
	"5.6": "2018-12-31",
	"7.0": "2019-01-10",
	"7.1": "2019-12-01",
	"7.2": "2020-11-30",
	"7.3": "2021-12-06",
	"7.4": "2022-11-28",
	"8.0": "2023-11-26",
	"8.1": "2025-12-31",
	"8.2": "2026-12-31",
	"8.3": "2027-12-31",
	"8.4": "2028-12-31",
	"8.5": "2029-12-31",
}

const (
	phpCheckEvery = 6 * time.Hour
	phpEndingSoon = 90 * 24 * time.Hour
	// Reminders while the problem stands. Weekly for an EOL branch: often
	// enough that it cannot be forgotten, rarely enough that it is not muted.
	phpEOLRepeat    = 7 * 24 * time.Hour
	phpEndingRepeat = 30 * 24 * time.Hour
)

// PHP finding statuses, most urgent first.
const (
	phpEOL       = "eol"        // no security fixes from anyone we know of
	phpEOLDistro = "eol-distro" // upstream ended, but the OS vendor's package may still be patched
	phpEnding    = "ending"     // still supported, ends within phpEndingSoon
	phpOK        = "ok"
)

type phpFinding struct {
	Key           string   `json:"key"`   // host:<branch> or server:<id>; also the dismissal key
	Where         string   `json:"where"` // host | server
	ServerID      string   `json:"server_id,omitempty"`
	ServerName    string   `json:"server_name,omitempty"`
	Version       string   `json:"version"`
	Branch        string   `json:"branch"`
	Status        string   `json:"status"`
	SecurityUntil string   `json:"security_until,omitempty"`
	DaysLeft      int      `json:"days_left"`
	Paths         []string `json:"paths,omitempty"`   // host: the binaries found
	Package       string   `json:"package,omitempty"` // host: the OS package and its version, when known
}

type phpCheckState struct {
	mu        sync.Mutex
	findings  []phpFinding
	checkedAt time.Time
	// probed remembers what each container answered, keyed by container id. A
	// container's PHP cannot change without the container being recreated, which
	// gives it a new id, so each one is asked once rather than every six hours —
	// most of a fleet has no PHP at all, and asking forty containers four times a
	// day to learn that again is pure noise in their logs.
	probed map[string]string
}

var (
	phpVersionLine = regexp.MustCompile(`(?m)^PHP ([0-9]+)\.([0-9]+)\.([0-9]+)`)
	phpVersionEnv  = regexp.MustCompile(`^([0-9]+)\.([0-9]+)(?:\.([0-9]+))?`)
	phpBinaryName  = regexp.MustCompile(`^php(-fpm|-cgi)?[0-9]*(\.[0-9]+)?$`)
)

// parsePHPVersionOutput reads the version from `php -v`. It wants the line that
// starts with "PHP x.y.z", not merely some output: `docker exec` into a container
// without PHP prints its own error on stdout, and treating "something came back"
// as an answer would turn every container on the box into a PHP finding.
func parsePHPVersionOutput(out string) (version, branch string) {
	m := phpVersionLine.FindStringSubmatch(out)
	if m == nil {
		return "", ""
	}
	return m[1] + "." + m[2] + "." + m[3], m[1] + "." + m[2]
}

// parsePHPVersionEnv reads the PHP_VERSION variable the official php images
// (and wordpress, phpmyadmin, adminer, which build on them) set.
func parsePHPVersionEnv(v string) (version, branch string) {
	m := phpVersionEnv.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return "", ""
	}
	return strings.TrimSpace(v), m[1] + "." + m[2]
}

// classifyPHP decides how urgent a branch is on a given day.
func classifyPHP(branch string, now time.Time) (status, until string, daysLeft int) {
	until, known := phpSecurityEnd[branch]
	if !known {
		if branchLess(newestPHPBranch(), branch) {
			return phpOK, "", 0 // newer than this table knows about
		}
		return phpEOL, "", 0 // older than the table, or a branch that never existed
	}
	end, err := time.Parse("2006-01-02", until)
	if err != nil {
		return phpOK, until, 0
	}
	// Supported through the whole of the last day.
	left := end.Add(24 * time.Hour).Sub(now)
	daysLeft = int(left.Hours() / 24)
	switch {
	case left <= 0:
		return phpEOL, until, 0
	case left <= phpEndingSoon:
		return phpEnding, until, daysLeft
	}
	return phpOK, until, daysLeft
}

func newestPHPBranch() string {
	newest := ""
	for b := range phpSecurityEnd {
		if newest == "" || branchLess(newest, b) {
			newest = b
		}
	}
	return newest
}

// branchLess compares "major.minor" numerically, so that 8.10 sorts after 8.9.
func branchLess(a, b string) bool {
	var am, an, bm, bn int
	fmt.Sscanf(a, "%d.%d", &am, &an) //nolint:errcheck
	fmt.Sscanf(b, "%d.%d", &bm, &bn) //nolint:errcheck
	if am != bm {
		return am < bm
	}
	return an < bn
}

// distroMaintained reports whether an OS package version is the distribution's
// own build — which the distribution keeps patching after php.net stops, for as
// long as that OS release is supported.
//
// This is not a nicety. Ubuntu 24.04 ships PHP 8.3.6 and fixes it under that
// same version number (8.3.6-0ubuntu0.24.04.12 on kw01, 2026-10-22), so judging
// it by upstream's numbers would call a patched host vulnerable. A build from a
// third-party repository (deb.sury.org, whose version strings carry "sury" or
// "gbp") follows upstream's end of life and gets no such grace.
func distroMaintained(pkgVersion string) bool {
	v := strings.ToLower(pkgVersion)
	if v == "" || strings.Contains(v, "sury") || strings.Contains(v, "gbp") {
		return false
	}
	return strings.Contains(v, "ubuntu") || debianSecurityBuild.MatchString(v)
}

var debianSecurityBuild = regexp.MustCompile(`[~+]deb[0-9]+u[0-9]+`)

// phpHostDirs are where a host's PHP binaries live: the distribution's paths,
// plus the control panels and the third-party repository that install PHP
// side by side under /opt.
var phpHostDirs = []string{"/usr/bin", "/usr/sbin", "/usr/local/bin", "/usr/local/sbin"}
var phpHostGlobs = []string{
	"/opt/plesk/php/*/bin",
	"/opt/cpanel/ea-php*/root/usr/bin",
	"/opt/cpanel/ea-php*/root/usr/sbin",
	"/opt/remi/php*/root/usr/bin",
	"/opt/remi/php*/root/usr/sbin",
}

// phpHostBinaries lists the PHP executables under dirs, one per real file:
// /usr/bin/php is usually a symlink to /usr/bin/php8.3, and the same binary
// should not be reported twice under two names.
func phpHostBinaries(dirs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !phpBinaryName.MatchString(e.Name()) {
				continue
			}
			p := filepath.Join(d, e.Name())
			real, err := filepath.EvalSymlinks(p)
			if err != nil {
				continue
			}
			st, err := os.Stat(real)
			if err != nil || st.IsDir() || st.Mode()&0o111 == 0 || seen[real] {
				continue
			}
			seen[real] = true
			out = append(out, real)
		}
	}
	sort.Strings(out)
	return out
}

// hostPHPProbe is what was learned about one binary on the host.
type hostPHPProbe struct {
	path, version, branch, pkg string
}

func probeHostPHP(ctx context.Context) []hostPHPProbe {
	dirs := append([]string{}, phpHostDirs...)
	for _, g := range phpHostGlobs {
		if m, err := filepath.Glob(g); err == nil {
			dirs = append(dirs, m...)
		}
	}
	var out []hostPHPProbe
	for _, bin := range phpHostBinaries(dirs) {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		b, _ := exec.CommandContext(c, bin, "-v").CombinedOutput()
		cancel()
		v, br := parsePHPVersionOutput(string(b))
		if v == "" {
			continue
		}
		out = append(out, hostPHPProbe{path: bin, version: v, branch: br, pkg: dpkgPackageFor(ctx, bin)})
	}
	return out
}

// dpkgPackageFor names the Debian/Ubuntu package that owns path, with its
// version ("php8.3-cli 8.3.6-0ubuntu0.24.04.12"), or "" when the host has no
// dpkg or the file belongs to no package.
func dpkgPackageFor(ctx context.Context, path string) string {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(c, "dpkg-query", "-S", path).Output()
	if err != nil {
		return ""
	}
	line := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)[0]
	pkg, _, ok := strings.Cut(line, ":")
	if !ok || strings.ContainsAny(pkg, " ,") {
		return "" // a diversion or several owners; not worth guessing
	}
	v, err := exec.CommandContext(c, "dpkg-query", "-W", "-f=${Version}", pkg).Output()
	if err != nil {
		return ""
	}
	return pkg + " " + strings.TrimSpace(string(v))
}

// hostPHPFindings folds the binaries into one finding per branch: php, php8.3
// and php-fpm8.3 are one PHP to upgrade, not three.
func hostPHPFindings(probes []hostPHPProbe, now time.Time) []phpFinding {
	byBranch := map[string]*phpFinding{}
	distro := map[string]bool{}
	for _, p := range probes {
		f := byBranch[p.branch]
		if f == nil {
			status, until, left := classifyPHP(p.branch, now)
			f = &phpFinding{Key: "host:" + p.branch, Where: "host", Version: p.version, Branch: p.branch,
				Status: status, SecurityUntil: until, DaysLeft: left}
			byBranch[p.branch] = f
			distro[p.branch] = true
		}
		f.Paths = append(f.Paths, p.path)
		if p.pkg != "" && f.Package == "" {
			f.Package = p.pkg
		}
		// The branch only counts as distribution-maintained if EVERY binary of it
		// is. One hand-built php-fpm beside the packaged CLI is unpatched.
		_, pkgVer, _ := strings.Cut(p.pkg, " ")
		if !distroMaintained(pkgVer) {
			distro[p.branch] = false
		}
	}
	var out []phpFinding
	for br, f := range byBranch {
		if distro[br] {
			switch f.Status {
			case phpEOL:
				f.Status = phpEOLDistro
			case phpEnding:
				// php.net's date is not the distribution's: Debian 12 keeps
				// patching its PHP 8.2 past 2026-12-31. A countdown to a day
				// on which nothing changes for this host would be false.
				f.Status = phpOK
			}
		}
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return branchLess(out[i].Branch, out[j].Branch) })
	return out
}

// containerPHP returns the PHP version a running container has, or "" for none.
// The image's PHP_VERSION answers for the official images without starting
// anything; anything else is asked directly. answered is false when Docker could
// not be asked at all — that is not the same as "no PHP", and must not be
// remembered as it.
func (s *Server) containerPHP(ctx context.Context, containerID string) (version string, answered bool) {
	info, err := s.docker.Inspect(ctx, containerID)
	if err != nil {
		return "", false
	}
	if info.Config != nil {
		for _, e := range info.Config.Env {
			if v, ok := strings.CutPrefix(e, "PHP_VERSION="); ok {
				if ver, _ := parsePHPVersionEnv(v); ver != "" {
					return ver, true
				}
			}
		}
	}
	if info.State == nil || !info.State.Running {
		return "", false // ask again once it is running
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	// A container without PHP is not an error here: the exec is created and
	// fails inside the container, with its message in the output.
	out, _, err := s.docker.ExecOutput(c, containerID, []string{"php", "-v"})
	if err != nil {
		return "", false
	}
	v, _ := parsePHPVersionOutput(out)
	return v, true
}

func (s *Server) serverPHPFindings(ctx context.Context, now time.Time) []phpFinding {
	if s.docker == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, name, container_id FROM servers WHERE status IN ('running','starting') AND COALESCE(container_id,'')<>''")
	if err != nil {
		return nil
	}
	type sv struct{ id, name, cid string }
	var list []sv
	for rows.Next() {
		var x sv
		if rows.Scan(&x.id, &x.name, &x.cid) == nil {
			list = append(list, x)
		}
	}
	rows.Close()

	s.phpCheck.mu.Lock()
	if s.phpCheck.probed == nil {
		s.phpCheck.probed = map[string]string{}
	}
	s.phpCheck.mu.Unlock()
	live := map[string]bool{}
	var out []phpFinding
	for _, x := range list {
		live[x.cid] = true
		s.phpCheck.mu.Lock()
		ver, seen := s.phpCheck.probed[x.cid]
		s.phpCheck.mu.Unlock()
		if !seen {
			var answered bool
			ver, answered = s.containerPHP(ctx, x.cid)
			if answered {
				s.phpCheck.mu.Lock()
				s.phpCheck.probed[x.cid] = ver
				s.phpCheck.mu.Unlock()
			}
		}
		if ver == "" {
			continue
		}
		_, br := parsePHPVersionEnv(ver)
		status, until, left := classifyPHP(br, now)
		out = append(out, phpFinding{Key: "server:" + x.id, Where: "server", ServerID: x.id, ServerName: x.name,
			Version: ver, Branch: br, Status: status, SecurityUntil: until, DaysLeft: left})
	}
	// Forget containers that are gone, so the cache cannot grow without bound.
	s.phpCheck.mu.Lock()
	for cid := range s.phpCheck.probed {
		if !live[cid] {
			delete(s.phpCheck.probed, cid)
		}
	}
	s.phpCheck.mu.Unlock()
	return out
}

func (s *Server) startPHPCheckLoop() {
	go func() {
		defer recoverLog("phpCheckLoop")
		// After startup has settled: autostart may still be recreating containers.
		time.Sleep(5 * time.Minute)
		for {
			s.checkPHP(context.Background())
			time.Sleep(phpCheckEvery)
		}
	}()
}

func (s *Server) checkPHP(ctx context.Context) {
	defer recoverLog("checkPHP")
	now := time.Now()
	findings := append(hostPHPFindings(probeHostPHP(ctx), now), s.serverPHPFindings(ctx, now)...)

	s.phpCheck.mu.Lock()
	s.phpCheck.findings, s.phpCheck.checkedAt = findings, now
	s.phpCheck.mu.Unlock()

	// One line per check, healthy or not: a quiet journal must be able to tell
	// "checked, all fine" apart from "never ran".
	var summary []string
	for _, f := range findings {
		summary = append(summary, fmt.Sprintf("%s=%s(%s)", phpWhere(f), f.Version, f.Status))
	}
	if len(summary) == 0 {
		summary = []string{"no PHP found"}
	}
	log.Printf("php check: %s", strings.Join(summary, ", "))

	for _, f := range findings {
		if f.Status == phpOK || s.phpDismissed(ctx, f) {
			continue
		}
		title, detail := phpMessage(f)
		repeat := phpEOLRepeat
		if f.Status != phpEOL {
			repeat = phpEndingRepeat
		}
		if s.raiseIncident(f.ServerID, "php-"+f.Status+"-"+f.Branch, title, detail, repeat) {
			icon := "🚨"
			if f.Status != phpEOL {
				icon = "🟠"
			}
			msg := icon + " " + title + "\n" + detail
			if f.ServerID != "" {
				go s.notifyServer(f.ServerID, msg)
			} else {
				go s.notifyAll(msg)
			}
		}
	}
}

func phpWhere(f phpFinding) string {
	if f.Where == "host" {
		return "the host"
	}
	return f.ServerName
}

// phpMessage is the notification text. It says what to do, because "PHP 8.1 is
// end of life" alone leaves the reader to work out whether that is their
// problem and how to fix it.
func phpMessage(f phpFinding) (title, detail string) {
	where := phpWhere(f)
	fix := "Recreate the server on an image with a supported PHP (Restart pulls the rune's image again), or change the rune to a newer PHP image."
	if f.Where == "host" {
		fix = "Upgrade PHP on the host (" + strings.Join(f.Paths, ", ") + ") to a supported branch, or remove it if nothing uses it."
	}
	switch f.Status {
	case phpEOL:
		title = fmt.Sprintf("EMERGENCY: %s runs PHP %s, which no longer gets security fixes", where, f.Version)
		since := ""
		if f.SecurityUntil != "" {
			since = " since " + f.SecurityUntil
		}
		detail = fmt.Sprintf("PHP %s has had no security support%s, so every vulnerability found in it stays open. %s",
			f.Branch, since, fix)
	case phpEOLDistro:
		title = fmt.Sprintf("%s runs PHP %s, whose upstream support has ended", where, f.Version)
		detail = fmt.Sprintf("php.net stopped fixing PHP %s on %s. It comes from the OS package %s, which the distribution may still be patching — but only while that OS release is supported. Check the release, and plan the move to a supported PHP.",
			f.Branch, f.SecurityUntil, f.Package)
	case phpEnding:
		title = fmt.Sprintf("%s runs PHP %s: security support ends in %d days", where, f.Version, f.DaysLeft)
		detail = fmt.Sprintf("PHP %s gets security fixes until %s. %s", f.Branch, f.SecurityUntil, fix)
	}
	return title, detail
}

// A dismissal is for one finding at one version. Upgrading to another version
// that is also unsupported raises it again; so does the same server moving to a
// different unsupported branch.
func phpAckKey(f phpFinding) string { return "php_ack_" + f.Key + "_" + f.Version }

func (s *Server) phpDismissed(ctx context.Context, f phpFinding) bool {
	return s.getSetting(ctx, phpAckKey(f)) == "1"
}

// handlePHPStatus lists every PHP the last check found, with what it means.
// Admin-only: host state, like the OS update status.
func (s *Server) handlePHPStatus(w http.ResponseWriter, r *http.Request) {
	s.phpCheck.mu.Lock()
	all := append([]phpFinding(nil), s.phpCheck.findings...)
	at := s.phpCheck.checkedAt
	s.phpCheck.mu.Unlock()

	findings := []phpFinding{}
	for _, f := range all {
		if f.Status == phpOK || s.phpDismissed(r.Context(), f) {
			continue
		}
		findings = append(findings, f)
	}
	sort.SliceStable(findings, func(i, j int) bool {
		return phpRank(findings[i].Status) < phpRank(findings[j].Status)
	})
	checked := ""
	if !at.IsZero() {
		checked = at.UTC().Format(time.RFC3339)
	}
	jsonOK(w, map[string]any{"checked_at": checked, "findings": findings, "all": all})
}

func phpRank(status string) int {
	switch status {
	case phpEOL:
		return 0
	case phpEOLDistro:
		return 1
	case phpEnding:
		return 2
	}
	return 3
}

// handleAckPHP dismisses one finding at its current version, for the whole panel.
func (s *Server) handleAckPHP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	s.phpCheck.mu.Lock()
	var found *phpFinding
	for i := range s.phpCheck.findings {
		if s.phpCheck.findings[i].Key == key {
			f := s.phpCheck.findings[i]
			found = &f
		}
	}
	s.phpCheck.mu.Unlock()
	if found == nil {
		jsonError(w, "no such PHP finding", http.StatusNotFound)
		return
	}
	s.setSetting(r.Context(), phpAckKey(*found), "1")
	s.auditLog(r, "php.ack", key, map[string]any{"version": found.Version, "status": found.Status})
	jsonOK(w, map[string]any{"ok": true})
}
