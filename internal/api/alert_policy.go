package api

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Alert policy — the layer between "a detector matched something" and "the
// admin's phone buzzes".
//
// Without it every detection pages: this panel once sent ~33 messages a day,
// each naming a different attacker IP and each asking for a decision, and the
// admin's fix was to delete every watcher — turning detection off entirely.
// That is the failure this file exists to prevent. Detection is only useful if
// it is safe to leave switched on.
//
// The policy is deliberately in code, not in the model's prompt. Whether a
// situation deserves a person's attention is a decision that must be
// reproducible, testable, and identical on every run; asking an LLM to make it
// would make the panel's paging behaviour depend on a sampling temperature.
// The model still explains what happened — it just no longer decides whether
// the admin hears about it.
//
// Two classes:
//
//	routine  — internet background radiation. Vulnerability scanners rattling
//	           door handles, answered by a 404 that already means "no". Recorded
//	           and summarised in the daily digest, never paged.
//	incident — something a person should look at now: one source hammering a
//	           login, or a flood large enough to matter regardless of shape.
//
// A distributed attack and a single-source attack need opposite responses, so
// the verdict says which it is. "Many sources, few tries each" cannot be fixed
// by blocking IPs one at a time — at its peak this panel saw 56 distinct IPs
// against one site in a day — and Kvasir is told so, rather than being left to
// propose 56 blocks.

type alertClass string

const (
	alertRoutine  alertClass = "routine"
	alertIncident alertClass = "incident"
)

const (
	// One situation pages at most once per this window. The dedupe key is
	// (server, situation), so a watcher that keeps re-tripping while an attack
	// runs is one message an hour, not one per scan tick.
	alertPageCooldown = time.Hour

	// Below this, nothing pages on its own. A handful of failed logins is a
	// normal day on the public internet.
	alertIncidentHits = 10

	// One source owning at least this share of the hits means blocking that
	// single address would actually stop it.
	alertConcentratedShare = 0.7

	// At or above this many distinct sources the situation is distributed:
	// per-IP blocking cannot win it.
	alertDistributedSources = 5

	// A distributed situation has to be this loud before it is worth waking
	// someone for, because the honest answer is "tighten the rate limit", not
	// "block these".
	alertDistributedHits = 100

	// Share of sample lines that must look like scanner probes before the whole
	// situation is called routine.
	alertNoiseShare = 0.8

	// Above this, volume alone makes it an incident even if every request is
	// being refused — at some point a flood is a flood regardless of the status
	// code. Set well above what ordinary scanning produces here: the noisiest
	// real spike this panel has recorded was 597 log lines in five minutes, and
	// the rest sat between 105 and 360.
	alertFloodHits = 1000

	// How many source addresses to keep on the record.
	alertMaxSources = 5
)

// alertInput is one detection, before anyone has decided whether it matters.
type alertInput struct {
	Key   string   // stable situation id, e.g. "watcher:<id>" or "anomaly:log-rate"
	Title string   // human summary, used as the notification's first line
	Hits  int      // how many times the thing matched
	Lines []string // sample of the matching log lines (may be a tail, not all of them)

	// Exempt are addresses that must never be treated as an attack source —
	// in practice the panel's own operators (see recentOperatorIPs). An admin
	// clicking through a WordPress dashboard looks exactly like a scraper:
	// many requests, one source, in a burst. Without this the policy reads
	// their own browsing as a concentrated attack and Kvasir proposes blocking
	// them out of their own site.
	Exempt map[string]bool
}

// alertVerdict is the policy's answer.
type alertVerdict struct {
	Class        alertClass
	Reason       string   // why, in words — recorded and shown to the admin
	Sources      []string // distinct public source addresses, busiest first
	Concentrated bool     // one source dominates, so blocking it would help
}

// ipRe finds IPv4 addresses in a log line. Deliberately not IPv6: every source
// address seen in practice here arrives via an access log's client field or an
// X-Forwarded-For, and a loose IPv6 pattern matches far too much other text
// (timestamps, hashes, container ids) to be worth the false positives.
var ipRe = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

// probePathRe matches paths that exist only in vulnerability scanners' wordlists.
// A request for /.env or /vendor/phpunit is never a real visitor.
//
// xmlrpc.php is deliberately absent: it is both a scanner target and the
// endpoint of a real brute-force this panel has actually weathered, so it is
// classified by volume and concentration rather than by name.
var probePathRe = regexp.MustCompile(`(?i)/(\.env|\.git/|\.aws/|wp-admin/setup-config\.php|phpunit|eval-stdin\.php|wlwmanifest\.xml|boaform|hnap1|cgi-bin/|autodiscover|shell\.php|backup\.(?:sql|zip|tar\.gz))`)

// probeStatusRe matches an access-log status field of 404 or 403 — the server
// already told the caller "no", which is the whole answer a scanner gets.
var probeStatusRe = regexp.MustCompile(`"\s+(?:404|403)\b`)

// classifyAlert decides whether a detection is worth a person's attention.
// Pure: the thresholds above are the entire policy, so a change of behaviour is
// a change to a named constant with a test, not a change of mood.
//
// Note on sampling: Hits is the detector's true count while Lines is often only
// a tail of the matches. Shares are therefore computed within the sample and
// treated as representative of the whole, which is what a tail of a burst is.
func classifyAlert(in alertInput) alertVerdict {
	sources, topShare := analyseSources(in.Lines, in.Exempt)
	noise := probeShare(in.Lines)
	hits := in.Hits
	if hits <= 0 {
		hits = len(in.Lines)
	}

	v := alertVerdict{Sources: sources}
	switch {
	// Volume first: past a point, a flood is a flood whatever the server answers.
	case hits >= alertFloodHits:
		v.Class = alertIncident
		v.Concentrated = topShare >= alertConcentratedShare && len(sources) > 0
		v.Reason = fmt.Sprintf("%d hits in the detection window — far beyond ordinary scanning, whatever the responses say", hits)

	// Then refusal. A scan the server answered with 404/403 is settled: the
	// answer was "no", and nothing about a person reading it makes it more
	// "no". This is deliberately ahead of the single-source test, because the
	// common case here is ONE scanner walking a wordlist — which is blockable
	// in principle but not worth a message, and treating it as an incident is
	// what buried the real alerts in noise.
	case noise >= alertNoiseShare:
		v.Class = alertRoutine
		v.Reason = fmt.Sprintf("%.0f%% of these are vulnerability scans the server already answered with 404/403 — background noise, recorded but not paged",
			noise*100)

	// Now single-source concentration, which at this point means traffic that
	// is largely getting THROUGH — a brute-force or scrape rather than a scan
	// being refused.
	case topShare >= alertConcentratedShare && hits >= alertIncidentHits:
		v.Class = alertIncident
		v.Concentrated = true
		v.Reason = fmt.Sprintf("%d hits and %.0f%% of them from %s, not being refused — a single source, so blocking it would stop this",
			hits, topShare*100, sources[0])

	case len(sources) >= alertDistributedSources && hits >= alertDistributedHits:
		v.Class = alertIncident
		v.Reason = fmt.Sprintf("%d hits spread across %d different sources — distributed, so blocking individual addresses will not stop it",
			hits, len(sources))

	case len(sources) >= alertDistributedSources:
		v.Class = alertRoutine
		v.Reason = fmt.Sprintf("%d hits thinly spread over %d sources — too little from any one of them to act on",
			hits, len(sources))

	case hits >= alertIncidentHits:
		v.Class = alertIncident
		v.Reason = fmt.Sprintf("%d hits in the detection window", hits)

	default:
		v.Class = alertRoutine
		v.Reason = fmt.Sprintf("only %d hits — below the threshold worth interrupting anyone for", hits)
	}
	return v
}

// analyseSources counts the public source addresses in the sample and returns
// them busiest-first along with the busiest one's share of all addresses found.
// Private, loopback, Cloudflare and Tailscale addresses are skipped: reusing
// checkBlockable keeps "what counts as a source" identical to "what we are
// willing to act on", so the classifier can never build a case around an
// address the firewall would refuse anyway.
func analyseSources(lines []string, exempt map[string]bool) (sources []string, topShare float64) {
	counts := map[string]int{}
	total := 0
	for _, l := range lines {
		seen := map[string]bool{}
		for _, m := range ipRe.FindAllString(l, -1) {
			if seen[m] { // one line, one vote per address
				continue
			}
			ip, err := checkBlockable(m)
			if err != nil {
				continue
			}
			if exempt[ip] {
				continue // one of ours — never counts toward an attack
			}
			seen[m] = true
			counts[ip]++
			total++
		}
	}
	if total == 0 {
		return nil, 0
	}
	type sc struct {
		ip string
		n  int
	}
	list := make([]sc, 0, len(counts))
	for ip, n := range counts {
		list = append(list, sc{ip, n})
	}
	// Sort by count, then address, so equal counts produce a stable order and
	// the same input always yields the same recorded sources.
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].ip < list[j].ip
	})
	topShare = float64(list[0].n) / float64(total)
	for i, e := range list {
		if i >= alertMaxSources {
			break
		}
		sources = append(sources, e.ip)
	}
	return sources, topShare
}

// probeShare is the fraction of non-empty sample lines that look like a
// vulnerability scan rather than traffic worth reading.
func probeShare(lines []string) float64 {
	total, probes := 0, 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		total++
		if probePathRe.MatchString(l) || probeStatusRe.MatchString(l) {
			probes++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(probes) / float64(total)
}

// alertHint turns a verdict into guidance for Kvasir's prompt, so the shape of
// the attack reaches the model as a fact rather than something it has to infer
// from a 20-line tail. Without it the model proposes what it always proposes —
// block this IP — which is the right answer for one attacker and useless
// against fifty.
func alertHint(v alertVerdict) string {
	switch {
	case v.Concentrated && len(v.Sources) > 0:
		return fmt.Sprintf("Nearly all of this traffic comes from the single address %s, so proposing block_ip for it is appropriate.", v.Sources[0])
	case len(v.Sources) >= alertDistributedSources:
		return fmt.Sprintf("This traffic comes from %d different source addresses with no dominant one. Do NOT propose block_ip: blocking addresses one at a time cannot stop a distributed attack. Propose a rate limit or a challenge at the edge instead, or action none if the server is coping.", len(v.Sources))
	default:
		return ""
	}
}

// raiseAlert classifies a detection, records it, and reports whether it should
// page the admin now. Every detector goes through here rather than calling
// notifyServer directly, so there is exactly one place that decides what is
// worth a message.
//
// Routine situations never page. Incidents page at most once per
// alertPageCooldown per (server, situation) — that dedupe is what turns "an
// attack is running" from a stream into a single message.
func (s *Server) raiseAlert(serverID string, in alertInput) (alertVerdict, bool) {
	if in.Exempt == nil {
		in.Exempt = s.recentOperatorIPs(context.Background())
	}
	v := classifyAlert(in)
	page := v.Class == alertIncident && !s.alertCoolingDown(serverID, in.Key)
	paged := 0
	if page {
		paged = 1
	}
	s.db.Exec(
		`INSERT INTO alerts (id, server_id, key, class, title, detail, sources, hits, reason, paged)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		uuid.New().String(), serverID, in.Key, string(v.Class), in.Title,
		firstLines(in.Lines, 5), strings.Join(v.Sources, ","), in.Hits, v.Reason, paged)
	return v, page
}

// alertCoolingDown reports whether this situation has already paged recently.
func (s *Server) alertCoolingDown(serverID, key string) bool {
	var n int
	s.db.QueryRow(
		`SELECT COUNT(*) FROM alerts WHERE server_id=? AND key=? AND paged=1
		 AND created_at >= datetime('now', ?)`,
		serverID, key, fmt.Sprintf("-%d minutes", int(alertPageCooldown.Minutes()))).Scan(&n) //nolint:errcheck
	return n > 0
}

// firstLines joins at most n sample lines for the record.
func firstLines(lines []string, n int) string {
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, strings.TrimSpace(l))
		if len(out) >= n {
			break
		}
	}
	return strings.Join(out, "\n")
}

// routineAlertSummary describes what was recorded but deliberately not sent, so
// the daily digest can say "here is what I handled quietly" instead of leaving
// the admin to wonder whether silence meant nothing happened or meant the
// detection had broken. Returns "" when there is nothing to report.
func (s *Server) routineAlertSummary(hours int) string {
	rows, err := s.db.Query(
		`SELECT COALESCE(title,''), COUNT(*), COALESCE(SUM(hits),0)
		 FROM alerts
		 WHERE paged=0 AND created_at >= datetime('now', ?)
		 GROUP BY key, title
		 ORDER BY SUM(hits) DESC
		 LIMIT 8`, fmt.Sprintf("-%d hours", hours))
	if err != nil {
		return ""
	}
	defer rows.Close()
	var lines []string
	situations, totalHits := 0, 0
	for rows.Next() {
		var title string
		var n, hits int
		if rows.Scan(&title, &n, &hits) != nil {
			continue
		}
		situations += n
		totalHits += hits
		lines = append(lines, fmt.Sprintf("• %s — %d× (%d hits)", title, n, hits))
	}
	if len(lines) == 0 {
		return ""
	}
	return fmt.Sprintf("🔕 Handled quietly: %d routine situations (%d hits) recorded without paging you\n%s",
		situations, totalHits, strings.Join(lines, "\n"))
}
