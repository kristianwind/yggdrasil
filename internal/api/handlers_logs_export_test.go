package api

import (
	"strings"
	"testing"
	"time"
)

// since/until reach the Docker API. Anything we don't recognise is refused
// rather than forwarded.
func TestParseSince(t *testing.T) {
	good := []string{"30s", "15m", "2h", "7d", "2026-07-16T10:00:00Z", "2026-07-16T10:00:00.5+02:00"}
	for _, v := range good {
		if got, err := parseSince(v); err != nil || got != v {
			t.Errorf("parseSince(%q) = (%q, %v), want it accepted unchanged", v, got, err)
		}
	}
	bad := []string{"yesterday", "1 hour", "-5m", "5x", "'; DROP TABLE", "2026-13-99", "all"}
	for _, v := range bad {
		if _, err := parseSince(v); err == nil {
			t.Errorf("parseSince(%q) was accepted", v)
		}
	}
	// Empty means "no bound", which is how the whole-log case is expressed.
	if got, err := parseSince(""); err != nil || got != "" {
		t.Errorf(`parseSince("") = (%q, %v), want ("", nil)`, got, err)
	}
}

func TestParseTail(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "all"},
		{"all", "all"},
		{"200", "200"},
		{"1", "1"},
	}
	for _, c := range cases {
		got, err := parseTail(c.in)
		if err != nil || got != c.want {
			t.Errorf("parseTail(%q) = (%q, %v), want %q", c.in, got, err, c.want)
		}
	}
	for _, v := range []string{"0", "-5", "abc", "1e6", "12.5"} {
		if _, err := parseTail(v); err == nil {
			t.Errorf("parseTail(%q) was accepted", v)
		}
	}
	// An absurd request is clamped rather than refused — the caller still gets a
	// file, just not one that asks Docker for every line it has ever held.
	got, err := parseTail("99999999")
	if err != nil {
		t.Fatalf("a large tail should clamp, not error: %v", err)
	}
	if got != "500000" {
		t.Errorf("parseTail(99999999) = %q, want it clamped to 500000", got)
	}
}

// The filename should say what the file is without being opened, and survive a
// server name that isn't filename-shaped.
func TestLogFilename(t *testing.T) {
	when := time.Date(2026, 7, 16, 14, 30, 5, 0, time.UTC)
	got := logFilename("Asgard", "console", when)
	if got != "asgard-console-20260716-143005.log" {
		t.Errorf("got %q", got)
	}
	// A name with spaces and a slash must not produce a path or a broken header.
	got = logFilename("My Server / test", "install", when)
	if strings.ContainsAny(got, "/\\ ") {
		t.Errorf("got %q, which is not safe as a filename", got)
	}
	if !strings.HasSuffix(got, "-install-20260716-143005.log") {
		t.Errorf("got %q, want the kind and timestamp preserved", got)
	}
	// An unnamed server still gets a usable name.
	if got := logFilename("", "console", when); !strings.HasPrefix(got, "server-console-") {
		t.Errorf("got %q, want a fallback base", got)
	}
}
