package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// Security advisories.
//
// The panel has no way to be contacted: there is no registry of installs, and
// deliberately never will be — knowing where the installs are is exactly the
// telemetry the project promises not to collect. So an urgent security notice
// cannot be pushed. It is PULLED, from a static file in the project's own
// repository, on the same six-hour cadence the release check already runs on.
//
// That choice has two consequences worth keeping:
//
//   - The request says nothing about the install. It is an anonymous GET for a
//     public file — no instance id, no version, no configuration. Nothing is
//     sent; something is fetched.
//   - It works for an install that has turned the beacon off. Carrying advisories
//     on the beacon would have been cheaper, but the beacon is opt-out and
//     permanently so, and the admin who switched it off is precisely the one who
//     still needs to hear about a vulnerability.
//
// An advisory is ADVISORY. It is text and a link that the panel renders as text.
// It is never a command, the panel never acts on it, and it cannot reach the
// self-update path — which installs a checksum-matched official release and
// nothing else. Anything that made an advisory actionable would turn this into a
// remote-execution channel for whoever controls the file.
const advisoriesURL = "https://raw.githubusercontent.com/kristianwind/yggdrasil/main/security/advisories.json"

// advisoryLinkHosts are the only hosts an advisory may link to. A security notice
// is the most attractive thing on the panel to hijack, so a link somewhere else is
// dropped and the advisory still shows — text without a link, rather than a
// trustworthy-looking banner pointing at somebody else's page.
var advisoryLinkHosts = map[string]bool{
	"github.com":         true,
	"www.github.com":     true,
	"yggdrasilpanel.com": true,
}

type advisory struct {
	ID       string `json:"id"`       // stable; also the dismissal key
	Severity string `json:"severity"` // critical | high | notice
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	// IntroducedIn is the first affected release (inclusive). Empty means every
	// version up to FixedIn is affected.
	IntroducedIn string `json:"introduced_in"`
	// FixedIn is the first release that is NOT affected (exclusive). Empty means
	// there is no fix yet — the advisory then applies to every version at or above
	// IntroducedIn, which is how a mitigation can be published before a patch.
	FixedIn string `json:"fixed_in"`
	URL     string `json:"url"`
}

type advisoryFile struct {
	Advisories []advisory `json:"advisories"`
}

var (
	advMu   sync.Mutex
	advList []advisory
	advAt   time.Time
)

// fetchAdvisories returns the published advisories, cached for six hours. A
// failure returns whatever is cached (possibly nothing) rather than an error:
// the banner is a courtesy, and a panel that cannot reach GitHub has bigger
// problems than a missing notice.
func fetchAdvisories(ctx context.Context) []advisory {
	advMu.Lock()
	defer advMu.Unlock()
	if advList != nil && time.Since(advAt) < 6*time.Hour {
		return advList
	}
	c, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, "GET", advisoriesURL, nil)
	if err != nil {
		return advList
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return advList
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return advList
	}
	var f advisoryFile
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
		return advList
	}
	advList, advAt = f.Advisories, time.Now()
	return advList
}

// advisoryApplies reports whether an advisory concerns the version running here.
//
// An unparseable version — a dev build, a hand-built binary — counts as affected.
// For a security notice the safe failure is to show it: a false alarm costs a
// dismissal, a missed one costs the install.
func advisoryApplies(a advisory, version string) bool {
	if a.ID == "" || a.Title == "" {
		return false
	}
	v := strings.TrimSpace(version)
	if v == "" || v == "dev" || !strings.HasPrefix(v, "v") {
		return true
	}
	if a.IntroducedIn != "" && semverLess(v, a.IntroducedIn) {
		return false // predates the flaw
	}
	if a.FixedIn != "" && !semverLess(v, a.FixedIn) {
		return false // already carries the fix
	}
	return true
}

// sanitizeAdvisoryLink keeps only links to the project's own places.
func sanitizeAdvisoryLink(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !advisoryLinkHosts[strings.ToLower(u.Host)] {
		return ""
	}
	return u.String()
}

func advisoryAckKey(id string) string { return "advisory_ack_" + id }

// handleAdvisories lists the advisories that apply to this build and have not
// been dismissed. Admin-only, because dismissing one is an admin decision and
// there is nothing here a non-admin could act on.
func (s *Server) handleAdvisories(w http.ResponseWriter, r *http.Request) {
	out := []advisory{}
	for _, a := range fetchAdvisories(r.Context()) {
		if !advisoryApplies(a, s.version) {
			continue
		}
		if s.getSetting(r.Context(), advisoryAckKey(a.ID)) == "1" {
			continue
		}
		a.URL = sanitizeAdvisoryLink(a.URL)
		out = append(out, a)
	}
	jsonOK(w, map[string]any{"advisories": out})
}

// handleAckAdvisory dismisses one advisory for the whole install. Like the beacon
// notice, this is a property of the panel rather than of the admin who happened to
// be looking: the point is that somebody dealt with it, not that everyone read it.
func (s *Server) handleAckAdvisory(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" || len(id) > 64 {
		jsonError(w, "bad advisory id", http.StatusBadRequest)
		return
	}
	s.setSetting(r.Context(), advisoryAckKey(id), "1")
	s.auditLog(r, "advisory.ack", id, nil)
	jsonOK(w, map[string]any{"ok": true})
}
