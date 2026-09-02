package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Operator-supplied analytics.
//
// The panel ships with none and will never phone home — that promise is about
// what THIS software does to the person running it. It says nothing about what
// that person may choose to measure on their own install, and until now they
// could not choose at all: the panel's Content-Security-Policy is
// `script-src 'self'`, so pasting a tag into the built index.html would have been
// silently blocked by the browser even if there had been anywhere to paste it.
//
// So this is two features, and the second is the one that makes the first work:
// a snippet stored per install, and a CSP that grows by exactly the origins that
// snippet needs. Get the first without the second and the field looks like it
// works, collects nothing, and the only evidence is in a console nobody opens.
//
// Nothing here is on by default. An empty setting injects nothing and leaves the
// CSP exactly as it was.
const analyticsSnippetKey = "analytics_head"

// analyticsMaxBytes bounds the snippet. Every analytics vendor's tag is a few
// hundred bytes; a limit this generous only ever catches a paste of the wrong
// thing, which is the mistake worth catching.
const analyticsMaxBytes = 4096

// Cached because both the header middleware and the SPA handler need it on every
// single request, and neither should touch SQLite to render a page.
var (
	anMu      sync.RWMutex
	anSnippet string
	anOrigins []string
	anLoaded  bool
)

var (
	srcAttrRe   = regexp.MustCompile(`(?i)\b(?:src|href)\s*=\s*["']([^"']+)["']`)
	scriptTagRe = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
)

// analyticsOrigins pulls the scheme://host out of every src/href in the snippet.
// Only absolute https/http URLs count: a relative path is same-origin and already
// allowed, and anything else (data:, javascript:) is not an origin we would widen
// the policy for.
func analyticsOrigins(snippet string) []string {
	seen := map[string]bool{}
	for _, m := range srcAttrRe.FindAllStringSubmatch(snippet, -1) {
		u, err := url.Parse(strings.TrimSpace(m[1]))
		if err != nil || u.Host == "" {
			continue
		}
		if u.Scheme != "https" && u.Scheme != "http" {
			continue
		}
		seen[u.Scheme+"://"+u.Host] = true
	}
	out := make([]string, 0, len(seen))
	for o := range seen {
		out = append(out, o)
	}
	sort.Strings(out) // stable, so the CSP header does not churn between restarts
	return out
}

// validateAnalyticsSnippet refuses what cannot be made to work, rather than
// storing it and letting the browser drop it in silence.
//
// Inline script bodies are the interesting case. Allowing them means adding
// 'unsafe-inline' to script-src, which switches off the single most useful
// protection the panel has — for every page, permanently, to run one vendor's
// bootstrap. That is a real decision and not one to make as a side effect of
// pasting into a text box, so for now it is refused with an explanation. Every
// self-hosted analytics tool worth having (Plausible, Umami, Fathom, GoatCounter,
// Matomo's cloud tag) ships a plain external script.
func validateAnalyticsSnippet(snippet string) error {
	if len(snippet) > analyticsMaxBytes {
		return fmt.Errorf("snippet is %d bytes; the limit is %d", len(snippet), analyticsMaxBytes)
	}
	if strings.Contains(strings.ToLower(snippet), "</head") {
		return fmt.Errorf("remove the </head> tag — the snippet is inserted inside <head> for you")
	}
	for _, m := range scriptTagRe.FindAllStringSubmatch(snippet, -1) {
		attrs, body := m[1], strings.TrimSpace(m[2])
		hasSrc := srcAttrRe.MatchString(attrs)
		if body != "" {
			return fmt.Errorf("inline <script> code is not supported: it would require " +
				"'unsafe-inline' in the panel's Content-Security-Policy, which weakens every " +
				"page permanently. Use a vendor tag that loads an external script instead " +
				"(Plausible, Umami, Fathom and GoatCounter all do)")
		}
		if !hasSrc {
			return fmt.Errorf("a <script> tag with neither src nor code does nothing")
		}
	}
	return nil
}

// loadAnalytics refreshes the cache from the database.
func (s *Server) loadAnalytics(ctx context.Context) {
	snippet := s.getSetting(ctx, analyticsSnippetKey)
	anMu.Lock()
	anSnippet = snippet
	anOrigins = analyticsOrigins(snippet)
	anLoaded = true
	anMu.Unlock()
}

func (s *Server) analyticsCached(ctx context.Context) (string, []string) {
	anMu.RLock()
	loaded, snippet, origins := anLoaded, anSnippet, anOrigins
	anMu.RUnlock()
	if !loaded {
		s.loadAnalytics(ctx)
		anMu.RLock()
		snippet, origins = anSnippet, anOrigins
		anMu.RUnlock()
	}
	return snippet, origins
}

// contentSecurityPolicy returns the CSP for this install: the strict default,
// widened by exactly the origins the operator's own snippet needs and nothing
// else. img-src is included because several vendors fall back to a tracking
// pixel, and connect-src because the whole point is to send the event somewhere.
func (s *Server) contentSecurityPolicy(ctx context.Context) string {
	_, origins := s.analyticsCached(ctx)
	self := ""
	if len(origins) > 0 {
		self = " " + strings.Join(origins, " ")
	}
	return "default-src 'self'; script-src 'self'" + self + "; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:" + self + "; font-src 'self'; " +
		"connect-src 'self'" + self + "; " +
		"frame-ancestors 'none'; base-uri 'self'; object-src 'none'"
}

// injectAnalytics places the snippet immediately before </head>.
func injectAnalytics(html, snippet string) string {
	if strings.TrimSpace(snippet) == "" {
		return html
	}
	i := strings.LastIndex(strings.ToLower(html), "</head>")
	if i < 0 {
		return html // no head to inject into; leave the document alone
	}
	return html[:i] + snippet + "\n" + html[i:]
}

type analyticsView struct {
	Snippet string   `json:"snippet"`
	Origins []string `json:"origins"`
}

func (s *Server) handleGetAnalytics(w http.ResponseWriter, r *http.Request) {
	snippet, origins := s.analyticsCached(r.Context())
	if origins == nil {
		origins = []string{}
	}
	jsonOK(w, analyticsView{Snippet: snippet, Origins: origins})
}

func (s *Server) handleSetAnalytics(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Snippet string `json:"snippet"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	snippet := strings.TrimSpace(req.Snippet)
	if snippet != "" {
		if err := validateAnalyticsSnippet(snippet); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	s.setSetting(r.Context(), analyticsSnippetKey, snippet)
	s.loadAnalytics(r.Context())

	// Audited without the snippet body: it is not a secret, but it is markup an
	// admin can point at any third party, and which origins it reaches is the part
	// worth being able to read back later.
	_, origins := s.analyticsCached(r.Context())
	s.auditLog(r, "settings.analytics", "", map[string]any{"origins": origins, "empty": snippet == ""})
	jsonOK(w, analyticsView{Snippet: snippet, Origins: origins})
}
