package api

import (
	"context"
	"strings"
	"testing"
)

// resetAnalyticsCache clears the package-level cache so tests do not inherit each
// other's state.
func resetAnalyticsCache() {
	anMu.Lock()
	anSnippet, anOrigins, anLoaded = "", nil, false
	anMu.Unlock()
}

// The tag Kristian actually pasted. If this one does not work, nothing does.
const plausibleTag = `<script defer data-domain="yggdrasilpanel.com" ` +
	`src="https://plausible.yggdrasilpanel.com/js/script.js"></script>`

func TestPlausibleTagIsAcceptedAndItsOriginExtracted(t *testing.T) {
	if err := validateAnalyticsSnippet(plausibleTag); err != nil {
		t.Fatalf("rejected the canonical Plausible tag: %v", err)
	}
	got := analyticsOrigins(plausibleTag)
	if len(got) != 1 || got[0] != "https://plausible.yggdrasilpanel.com" {
		t.Errorf("origins = %v, want [https://plausible.yggdrasilpanel.com] — the scheme and host only, no path", got)
	}
}

// The snippet is useless unless the policy lets the browser fetch it AND send the
// event. Both directions, or the field looks like it works and collects nothing.
func TestPolicyAllowsTheSnippetToLoadAndToSend(t *testing.T) {
	resetAnalyticsCache()
	defer resetAnalyticsCache()
	s := testServer(t)
	s.setSetting(context.Background(), analyticsSnippetKey, plausibleTag)

	csp := s.contentSecurityPolicy(context.Background())
	const origin = "https://plausible.yggdrasilpanel.com"
	for _, directive := range []string{"script-src", "connect-src", "img-src"} {
		seg := cspDirective(csp, directive)
		if !strings.Contains(seg, origin) {
			t.Errorf("%s = %q, want it to include %s", directive, seg, origin)
		}
	}
	// Widened by exactly the one origin, and nowhere else.
	if strings.Contains(cspDirective(csp, "default-src"), origin) {
		t.Error("default-src was widened; only script/connect/img should be")
	}
	if strings.Contains(csp, "'unsafe-inline'") && !strings.Contains(cspDirective(csp, "style-src"), "'unsafe-inline'") {
		t.Error("'unsafe-inline' leaked outside style-src")
	}
}

// The 22 installs that never touch this field must get the policy they got
// before it existed — byte for byte.
func TestPolicyIsUnchangedWithoutASnippet(t *testing.T) {
	resetAnalyticsCache()
	defer resetAnalyticsCache()
	s := testServer(t)

	const before = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'self'; object-src 'none'"
	if got := s.contentSecurityPolicy(context.Background()); got != before {
		t.Errorf("policy changed for an install with no snippet.\n got: %q\nwant: %q", got, before)
	}
}

func TestInlineScriptIsRefusedWithAReason(t *testing.T) {
	err := validateAnalyticsSnippet(`<script>window.foo=1;</script>`)
	if err == nil {
		t.Fatal("inline script accepted; it would silently never run under script-src 'self'")
	}
	if !strings.Contains(err.Error(), "unsafe-inline") {
		t.Errorf("error should explain the cost, got %q", err)
	}
}

func TestSnippetGuards(t *testing.T) {
	if err := validateAnalyticsSnippet(strings.Repeat("x", analyticsMaxBytes+1)); err == nil {
		t.Error("oversized snippet accepted")
	}
	if err := validateAnalyticsSnippet(`<script src="/x.js"></script></head>`); err == nil {
		t.Error("a snippet closing </head> accepted; it would truncate the document")
	}
	if err := validateAnalyticsSnippet(`<script></script>`); err == nil {
		t.Error("a script with neither src nor body accepted")
	}
}

// Relative and non-http sources are not origins to widen the policy for: a
// relative path is already same-origin, and data:/javascript: are not fetches.
func TestOnlyAbsoluteHTTPOriginsWidenThePolicy(t *testing.T) {
	got := analyticsOrigins(`<script src="/local.js"></script><img src="data:image/gif;base64,x">`)
	if len(got) != 0 {
		t.Errorf("origins = %v, want none", got)
	}
}

func TestInjectionGoesInsideHead(t *testing.T) {
	const doc = "<html><head><title>x</title></head><body>b</body></html>"
	out := injectAnalytics(doc, plausibleTag)
	head := out[:strings.Index(out, "</head>")]
	if !strings.Contains(head, plausibleTag) {
		t.Errorf("snippet not inside <head>:\n%s", out)
	}
	if strings.Count(out, "</head>") != 1 {
		t.Errorf("document has %d </head> tags, want 1", strings.Count(out, "</head>"))
	}
	if got := injectAnalytics(doc, ""); got != doc {
		t.Error("an empty snippet must leave the document untouched")
	}
	// A document with no head is left alone rather than half-rewritten.
	const noHead = "<html><body>b</body></html>"
	if got := injectAnalytics(noHead, plausibleTag); got != noHead {
		t.Error("a document without </head> must be left alone")
	}
}

// cspDirective returns one directive's value from a policy string.
func cspDirective(policy, name string) string {
	for _, part := range strings.Split(policy, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, name+" ") {
			return part
		}
	}
	return ""
}
