package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestClassifyPHP(t *testing.T) {
	now := day("2026-10-22 12:00")
	for _, c := range []struct {
		branch, want string
	}{
		{"5.6", phpEOL},
		{"7.4", phpEOL},
		{"8.0", phpEOL},
		{"8.1", phpEOL},    // ended 2025-12-31
		{"8.2", phpEnding}, // ends 2026-12-31: 70 days away
		{"8.3", phpOK},
		{"8.5", phpOK},
		{"8.6", phpOK},  // newer than the table: supported, not an emergency
		{"9.0", phpOK},  // likewise
		{"5.4", phpEOL}, // older than the table
		{"7.5", phpEOL}, // never existed, sits among ended branches
	} {
		got, _, _ := classifyPHP(c.branch, now)
		if got != c.want {
			t.Errorf("classifyPHP(%s) = %s, want %s", c.branch, got, c.want)
		}
	}
}

// The last day of support is still a supported day; the next one is not.
func TestClassifyPHPEdgeOfTheLastDay(t *testing.T) {
	if got, _, _ := classifyPHP("8.1", day("2025-12-31 23:00")); got != phpEnding {
		t.Errorf("on its last day 8.1 = %s, want ending", got)
	}
	if got, _, _ := classifyPHP("8.1", day("2026-01-01 00:30")); got != phpEOL {
		t.Errorf("the day after, 8.1 = %s, want eol", got)
	}
	if _, _, left := classifyPHP("8.2", day("2026-10-22 12:00")); left != 70 {
		t.Errorf("days left for 8.2 on 2026-10-22 = %d, want 70", left)
	}
}

func TestParsePHPVersionOutput(t *testing.T) {
	for out, want := range map[string]string{
		"PHP 8.3.35 (cli) (built: Oct  6 2026 01:28:49) (NTS)\nCopyright (c) The PHP Group": "8.3.35",
		"PHP 8.1.2-1ubuntu2.20 (cli) (built: ...)":                                          "8.1.2",
		"PHP 8.2.28 (fpm-fcgi) (built: Mar 13 2025)":                                        "8.2.28",
		// What `docker exec` prints for a container with no PHP — measured on .164.
		// "Some output came back" must not count as a PHP.
		`OCI runtime exec failed: exec failed: unable to start container process: exec: "php": executable file not found in $PATH`: "",
		"": "",
	} {
		if got, _ := parsePHPVersionOutput(out); got != want {
			t.Errorf("parsePHPVersionOutput(%q) = %q, want %q", out, got, want)
		}
	}
	if v, br := parsePHPVersionEnv("8.3.35"); v != "8.3.35" || br != "8.3" {
		t.Errorf("env 8.3.35 → %q %q", v, br)
	}
	if v, _ := parsePHPVersionEnv("latest"); v != "" {
		t.Errorf("a non-version PHP_VERSION must be ignored, got %q", v)
	}
}

func TestDistroMaintained(t *testing.T) {
	for v, want := range map[string]bool{
		"8.3.6-0ubuntu0.24.04.12":                     true, // kw01, measured 2026-10-22
		"8.1.2-1ubuntu2.20":                           true,
		"8.2.28-1~deb12u1":                            true,
		"7.4.33-1+deb11u7":                            true,
		"8.1.31-1+ubuntu22.04.1+deb.sury.org+1":       false, // third-party: follows upstream EOL
		"8.1.31-1+0~20241121.92+debian12~1.gbp3c4e5a": false,
		"8.4.11-1": false, // no vendor marker: judged by upstream
		"":         false,
	} {
		if got := distroMaintained(v); got != want {
			t.Errorf("distroMaintained(%q) = %v, want %v", v, got, want)
		}
	}
}

// php, php8.1 and php-fpm8.1 on one host are one PHP to upgrade.
func TestHostFindingsOnePerBranch(t *testing.T) {
	now := day("2026-10-22 12:00")
	got := hostPHPFindings([]hostPHPProbe{
		{path: "/usr/bin/php8.1", version: "8.1.31", branch: "8.1", pkg: "php8.1-cli 8.1.31-1+ubuntu22.04.1+deb.sury.org+1"},
		{path: "/usr/sbin/php-fpm8.1", version: "8.1.31", branch: "8.1", pkg: "php8.1-fpm 8.1.31-1+ubuntu22.04.1+deb.sury.org+1"},
		{path: "/usr/bin/php8.3", version: "8.3.6", branch: "8.3", pkg: "php8.3-cli 8.3.6-0ubuntu0.24.04.12"},
	}, now)
	if len(got) != 2 {
		t.Fatalf("want one finding per branch, got %d: %+v", len(got), got)
	}
	if got[0].Branch != "8.1" || got[0].Status != phpEOL || len(got[0].Paths) != 2 || got[0].Key != "host:8.1" {
		t.Errorf("8.1 finding = %+v, want eol with both binaries", got[0])
	}
	if got[1].Status != phpOK {
		t.Errorf("8.3 = %s, want ok", got[1].Status)
	}
}

// An Ubuntu-packaged EOL branch is a warning, not an emergency — unless any of
// its binaries is not the distribution's.
func TestHostDistroPHPIsNotAnEmergency(t *testing.T) {
	now := day("2026-10-22 12:00")
	got := hostPHPFindings([]hostPHPProbe{
		{path: "/usr/bin/php8.1", version: "8.1.2", branch: "8.1", pkg: "php8.1-cli 8.1.2-1ubuntu2.20"},
	}, now)
	if len(got) != 1 || got[0].Status != phpEOLDistro {
		t.Fatalf("Ubuntu's own 8.1 = %+v, want eol-distro", got)
	}
	// claude-rc, measured 2026-10-22: Debian 12's PHP 8.2 is not "ending in 70
	// days" — Debian goes on patching it after php.net's date.
	got = hostPHPFindings([]hostPHPProbe{
		{path: "/usr/bin/php8.2", version: "8.2.35", branch: "8.2", pkg: "php8.2-cli 8.2.35-1~deb12u1"},
	}, now)
	if len(got) != 1 || got[0].Status != phpOK {
		t.Fatalf("Debian's own 8.2 = %+v, want ok", got)
	}
	got = hostPHPFindings([]hostPHPProbe{
		{path: "/usr/bin/php8.1", version: "8.1.2", branch: "8.1", pkg: "php8.1-cli 8.1.2-1ubuntu2.20"},
		{path: "/usr/local/sbin/php-fpm", version: "8.1.2", branch: "8.1"}, // built by hand
	}, now)
	if len(got) != 1 || got[0].Status != phpEOL {
		t.Fatalf("with an unpackaged binary beside it, 8.1 = %+v, want eol", got)
	}
}

func TestPHPHostBinaries(t *testing.T) {
	d := t.TempDir()
	write := func(name string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(d, name), []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("php8.1", 0o755)
	write("php-fpm8.1", 0o755)
	write("phpize8.1", 0o755)  // a build tool, not a PHP
	write("php-config", 0o755) // likewise
	write("phpdbg8.1", 0o755)  // a debugger; reports the same branch as php8.1
	write("php8.2", 0o644)     // not executable
	if err := os.Symlink(filepath.Join(d, "php8.1"), filepath.Join(d, "php")); err != nil {
		t.Fatal(err)
	}
	got := phpHostBinaries([]string{d, filepath.Join(d, "missing")})
	want := []string{filepath.Join(d, "php-fpm8.1"), filepath.Join(d, "php8.1")}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("phpHostBinaries = %v, want %v (the php symlink counted once, tools skipped)", got, want)
	}
}

// End to end through the real probe: fake PHP binaries on disk answer -v, and
// an EOL one produces an incident and a finding the API shows.
func TestCheckPHPRaisesOnceAndCanBeDismissed(t *testing.T) {
	s := testServer(t)
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "php8.0"), []byte("#!/bin/sh\necho 'PHP 8.0.30 (cli) (built: Sep  1 2023)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := phpHostDirs
	phpHostDirs = []string{d}
	t.Cleanup(func() { phpHostDirs = old })

	ctx := context.Background()
	s.checkPHP(ctx)
	s.checkPHP(ctx) // the second check is within the repeat window: no second page

	var paged, total int
	s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(paged),0) FROM alerts WHERE key='php-eol-8.0'`).Scan(&total, &paged) //nolint:errcheck
	if total != 2 || paged != 1 {
		t.Errorf("alerts for php-eol-8.0: %d rows, %d paged; want 2 rows, 1 paged", total, paged)
	}

	list := func() []phpFinding {
		rec := httptest.NewRecorder()
		s.handlePHPStatus(rec, httptest.NewRequest("GET", "/api/system/php", nil))
		var body struct {
			Findings []phpFinding `json:"findings"`
		}
		json.Unmarshal(rec.Body.Bytes(), &body) //nolint:errcheck
		return body.Findings
	}
	f := list()
	if len(f) != 1 || f[0].Status != phpEOL || f[0].Version != "8.0.30" || f[0].Where != "host" {
		t.Fatalf("findings = %+v, want the host's PHP 8.0.30 as eol", f)
	}
	if title, _ := phpMessage(f[0]); !strings.HasPrefix(title, "EMERGENCY") {
		t.Errorf("an EOL PHP must be announced as an emergency, title = %q", title)
	}

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("key", "host:8.0")
	req := httptest.NewRequest("POST", "/api/system/php/host:8.0/ack", nil).
		WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	s.handleAckPHP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body)
	}
	if f := list(); len(f) != 0 {
		t.Errorf("after dismissal the finding is still listed: %+v", f)
	}

	// A different, still unsupported version is a new finding.
	os.WriteFile(filepath.Join(d, "php8.0"), []byte("#!/bin/sh\necho 'PHP 8.0.31 (cli)'\n"), 0o755) //nolint:errcheck
	s.checkPHP(ctx)
	if f := list(); len(f) != 1 || f[0].Version != "8.0.31" {
		t.Errorf("a new unsupported version must show again, got %+v", f)
	}
}
