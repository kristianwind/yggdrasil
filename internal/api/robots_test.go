package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 🔴 The bug this guards is not "robots.txt says the wrong thing" — it is
// robots.txt returning the SPA's index.html with HTTP 200, because the catch-all
// answers every unknown path. A crawler reads HTML where it asked for rules as
// "there are no rules", i.e. crawl everything, which is how a production panel
// became the first Google result for the product's own name.
//
// So the assertion is on the CONTENT TYPE and the body, not just the status.
func TestRobotsTxtIsNotTheSPA(t *testing.T) {
	rec := httptest.NewRecorder()
	handleRobotsTxt(rec, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q; HTML here means the SPA answered and the rules were never read", ct)
	}
	body := rec.Body.String()
	if strings.Contains(strings.ToLower(body), "<!doctype") || strings.Contains(body, "<html") {
		t.Fatalf("the SPA answered instead of the rules: %.60q", body)
	}
	if !strings.Contains(body, "User-agent: *") || !strings.Contains(body, "Disallow: /") {
		t.Errorf("must disallow everything, got:\n%s", body)
	}
}

// A crawler that ignores robots.txt still honours the header, and unlike the
// meta tag it covers the API and every file — not only rendered pages.
func TestEveryResponseCarriesNoIndex(t *testing.T) {
	h := noIndexHeader(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, path := range []string{"/", "/api/version", "/assets/app.js", "/login"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		got := rec.Header().Get("X-Robots-Tag")
		if !strings.Contains(got, "noindex") {
			t.Errorf("%s: X-Robots-Tag = %q, want noindex", path, got)
		}
	}
}

// robots.txt must carry the header too: a crawler that fetches only that file
// should still be told the whole host is off limits.
func TestRobotsTxtItselfIsNoIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	handleRobotsTxt(rec, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))
	if !strings.Contains(rec.Header().Get("X-Robots-Tag"), "noindex") {
		t.Error("robots.txt should carry X-Robots-Tag as well")
	}
}
