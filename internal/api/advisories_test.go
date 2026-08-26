package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// The version filter is the whole safety of the feature: an advisory that keeps
// showing after the fix trains people to dismiss without reading, and one that
// stops showing too early is worse.
func TestAdvisoryApplies(t *testing.T) {
	a := advisory{ID: "2026-001", Title: "x", IntroducedIn: "v0.2.100", FixedIn: "v0.3.2"}

	cases := []struct {
		version string
		want    bool
		why     string
	}{
		{"v0.2.99", false, "predates the flaw"},
		{"v0.2.100", true, "first affected release, inclusive"},
		{"v0.3.1", true, "in range"},
		{"v0.3.2", false, "carries the fix, exclusive"},
		{"v0.4.0", false, "well past the fix"},
		{"dev", true, "a dev build cannot be cleared, so show it"},
		{"", true, "unknown version — fail loud"},
		{"garbage", true, "unparseable — fail loud"},
	}
	for _, c := range cases {
		if got := advisoryApplies(a, c.version); got != c.want {
			t.Errorf("version %q: got %v, want %v (%s)", c.version, got, c.want, c.why)
		}
	}

	// No fix yet: everything from IntroducedIn onwards is affected, which is how a
	// mitigation gets published before a patch exists.
	open := advisory{ID: "x", Title: "x", IntroducedIn: "v0.3.0"}
	if !advisoryApplies(open, "v9.9.9") {
		t.Error("an advisory with no fixed_in must still apply to the newest build")
	}
	if advisoryApplies(open, "v0.2.0") {
		t.Error("an advisory with no fixed_in must still respect introduced_in")
	}

	// A malformed entry is ignored rather than shown as an empty banner.
	if advisoryApplies(advisory{Title: "no id"}, "v0.3.0") {
		t.Error("an advisory without an id must be ignored")
	}
	if advisoryApplies(advisory{ID: "no-title"}, "v0.3.0") {
		t.Error("an advisory without a title must be ignored")
	}
}

// A security banner is the most attractive thing on the panel to hijack, so a
// link anywhere but the project's own places is dropped — the advisory still
// shows, without a link.
func TestSanitizeAdvisoryLink(t *testing.T) {
	keep := []string{
		"https://github.com/kristianwind/yggdrasil/security/advisories/GHSA-x",
		"https://yggdrasilpanel.com/docs/",
	}
	for _, u := range keep {
		if got := sanitizeAdvisoryLink(u); got != u {
			t.Errorf("expected %q to be kept, got %q", u, got)
		}
	}
	drop := []string{
		"http://github.com/kristianwind/yggdrasil", // not https
		"https://github.com.evil.example/x",        // suffix trick
		"https://evil.example/patch.sh",            // elsewhere entirely
		"javascript:alert(1)",                      // not a link at all
		"https://raw.githubusercontent.com/x/y/z",  // not on the allowlist
		"",
	}
	for _, u := range drop {
		if got := sanitizeAdvisoryLink(u); got != "" {
			t.Errorf("expected %q to be dropped, got %q", u, got)
		}
	}
}

// End-to-end through the handler: the cache is seeded so no network is touched,
// and the response must contain only what this build should see — filtered by
// version, minus anything dismissed, with off-site links stripped.
func TestHandleAdvisoriesFiltersAndDismisses(t *testing.T) {
	s := testServer(t)
	s.version = "v0.3.1"

	advMu.Lock()
	advList = []advisory{
		{ID: "old", Title: "Already fixed", FixedIn: "v0.3.0"},
		{ID: "live", Title: "Applies here", Severity: "critical", FixedIn: "v0.3.2",
			URL: "https://github.com/kristianwind/yggdrasil/security/advisories/GHSA-1"},
		{ID: "offsite", Title: "Applies, bad link", FixedIn: "v0.3.2", URL: "https://evil.example/x"},
	}
	advAt = time.Now()
	advMu.Unlock()
	t.Cleanup(func() {
		advMu.Lock()
		advList, advAt = nil, time.Time{}
		advMu.Unlock()
	})

	get := func() []advisory {
		t.Helper()
		rec := httptest.NewRecorder()
		s.handleAdvisories(rec, httptest.NewRequest("GET", "/api/advisories", nil))
		if rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
		var body struct {
			Advisories []advisory `json:"advisories"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body.Advisories
	}

	got := get()
	if len(got) != 2 {
		t.Fatalf("expected the 2 applicable advisories, got %d: %+v", len(got), got)
	}
	byID := map[string]advisory{}
	for _, a := range got {
		byID[a.ID] = a
	}
	if _, fixed := byID["old"]; fixed {
		t.Error("an advisory fixed before this build must not be shown")
	}
	if byID["live"].URL == "" {
		t.Error("a link to the project's own repo must survive")
	}
	if byID["offsite"].URL != "" {
		t.Errorf("an off-site link must be dropped, got %q", byID["offsite"].URL)
	}

	// Dismissing is per install and sticks.
	s.setSetting(context.Background(), advisoryAckKey("live"), "1")
	got = get()
	if len(got) != 1 || got[0].ID != "offsite" {
		t.Fatalf("dismissed advisory still shown: %+v", got)
	}
}
