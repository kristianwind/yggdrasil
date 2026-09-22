package api

import "net/http"

// Keeping the panel out of search engines.
//
// A control panel is not a website. Its login page names the software and its
// version, its hostname tells an attacker where a Yggdrasil install lives, and
// none of it is content anybody searched for. Yet a panel reachable over https
// — which the MCP connector and passkeys both require — is crawlable by
// default, and Google had indexed a production install as the FIRST result for
// "yggdrasil panel", above the project's own website.
//
// Nothing was misconfigured. There was simply nothing saying no.
//
// 🔴 The route must be registered BEFORE the SPA catch-all. Asking for
// /robots.txt used to return index.html with HTTP 200, because the fallback
// answers every unknown path — and HTML where a crawler asked for rules is read
// as "no robots.txt", which means crawl everything. The same shape cost four
// hours on mimir.guide once already.
//
// Belt and braces, because each covers what the others cannot:
//   - robots.txt asks a well-behaved crawler not to fetch.
//   - X-Robots-Tag tells one that fetched anyway not to index, and applies to
//     every response, not just HTML.
//   - The meta tag in index.html covers a crawler that renders the SPA.
//
// This is deliberately not configurable. An operator who genuinely wants their
// panel in Google can put it there from outside; shipping the choice as a
// setting would mean shipping installs that are indexable by accident, which is
// exactly what happened.
func handleRobotsTxt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	// Short, because a crawler caches this and an operator who moves the panel
	// behind a different hostname should not wait a week for it to be re-read.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte("# A Yggdrasil panel is not a website. Nothing here is for search engines.\nUser-agent: *\nDisallow: /\n"))
}

// noIndexHeader adds X-Robots-Tag to every response. A crawler that ignored
// robots.txt still honours this, and unlike the meta tag it covers the API and
// any file the panel serves — not only pages that get rendered.
func noIndexHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		next.ServeHTTP(w, r)
	})
}
