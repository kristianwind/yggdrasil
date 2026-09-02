package api

import (
	"net/http"
	"strings"
)

// Demo mode: a panel anyone may look at and nobody may change.
//
// The obvious implementation — give the public a view-only account — does not
// work, because the pages most worth showing (Statistics, Settings) are
// admin-only and a delegate cannot see them at all. So the demo signs in as a
// real admin and the panel refuses to act.
//
// That inverts where the safety lives, and it is worth being clear-eyed about
// what that means. This panel's entire job is driving Docker: it holds a socket
// to the host's container runtime. Demo mode is a POLICY layer over that
// capability, and policy layers have holes. If one mutating route slips past the
// guard below, what a stranger reaches is not "edit a demo record" — it is
// "create a container on the host".
//
// Hence two rules that are not negotiable:
//
//  1. The guard is a DENY-LIST OF NOTHING: everything that is not a plain read is
//     refused, and the handful of exceptions are named here in one place where
//     they can be counted. Adding a route cannot accidentally make it writable.
//  2. It runs on a machine that hosts nothing else, and gets restored from a
//     snapshot on a schedule — so a hole in this file is bounded in time and
//     blast radius rather than being the only thing standing between the public
//     and a container runtime.
//
// Rule 2 is not this file's job, but this file is the reason it exists.

// demoAllowedMutations are the only state-changing requests a demo panel serves.
//
// Login only. Logout is deliberately refused: it revokes every session for that
// account, so one visitor pressing Sign out would eject everyone else looking at
// the demo. The web UI already discards the local token whether or not that call
// succeeds, so the button still behaves correctly for the person who pressed it.
var demoAllowedMutations = map[string]bool{
	"/api/auth/login": true,
}

// demoMode reports whether this panel is a public demonstration.
func (s *Server) demoMode() bool { return s.cfg != nil && s.cfg.Server.DemoMode }

// demoGuard refuses anything that could change state.
//
// Method-based filtering alone is not enough and that is the trap worth naming:
// the console is a WebSocket opened with GET that then writes to the container's
// stdin, and the Kvasir chat is a GET that spends the operator's own LLM credits
// on whoever is typing. Neither is caught by "block POST". They are handled at
// their handlers, and listed here so the next reader knows the set is bigger than
// the verb.
func (s *Server) demoGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.demoMode() {
			next.ServeHTTP(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if demoAllowedMutations[strings.TrimSuffix(r.URL.Path, "/")] {
			next.ServeHTTP(w, r)
			return
		}
		jsonError(w, "This is a public demo — it shows real data but nothing can be changed.",
			http.StatusForbidden)
	})
}
