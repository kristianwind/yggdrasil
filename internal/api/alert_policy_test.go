package api

import (
	"fmt"
	"strings"
	"testing"
)

// The classifier is the whole alert policy, so these cases are written from the
// traffic this panel actually saw: distributed xmlrpc brute force (56 IPs in a
// day against one site), a single-source wp-login hammer, and the endless 404
// scanning that made the admin delete every watcher.

func accessLine(ip, path, status string) string {
	return fmt.Sprintf(`%s - - [30/Jul/2026:04:12:11 +0000] "POST %s HTTP/1.1" %s 3243 "-" "Mozilla/5.0"`, ip, path, status)
}

func TestClassifyAlertSingleSourceIsIncidentAndBlockable(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, accessLine("149.36.51.138", "/wp-login.php", "200"))
	}
	v := classifyAlert(alertInput{Key: "watcher:x", Hits: 20, Lines: lines})

	if v.Class != alertIncident {
		t.Fatalf("one source hammering wp-login should be an incident, got %q (%s)", v.Class, v.Reason)
	}
	if !v.Concentrated {
		t.Error("a single dominant source must be marked concentrated — that is what makes block_ip the right proposal")
	}
	if len(v.Sources) != 1 || v.Sources[0] != "149.36.51.138" {
		t.Errorf("sources = %v, want just the attacker", v.Sources)
	}
	if hint := alertHint(v); !strings.Contains(hint, "149.36.51.138") {
		t.Errorf("hint should name the address to block, got %q", hint)
	}
}

func TestClassifyAlertDistributedIsIncidentButNotBlockable(t *testing.T) {
	// 12 sources × 10 hits: loud enough to matter, too spread out to block.
	var lines []string
	for i := 0; i < 12; i++ {
		for j := 0; j < 10; j++ {
			lines = append(lines, accessLine(fmt.Sprintf("203.0.113.%d", i+1), "/xmlrpc.php", "200"))
		}
	}
	v := classifyAlert(alertInput{Key: "watcher:x", Hits: 120, Lines: lines})

	if v.Class != alertIncident {
		t.Fatalf("a 120-hit flood should be an incident, got %q (%s)", v.Class, v.Reason)
	}
	if v.Concentrated {
		t.Error("no single source dominates, so this must NOT be marked concentrated")
	}
	hint := alertHint(v)
	if !strings.Contains(hint, "Do NOT propose block_ip") {
		t.Errorf("a distributed attack must steer Kvasir away from per-IP blocking, got %q", hint)
	}
}

func TestClassifyAlertScannerNoiseIsRoutine(t *testing.T) {
	lines := []string{
		accessLine("198.51.100.7", "/.env", "404"),
		accessLine("198.51.100.9", "/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php", "404"),
		accessLine("198.51.100.11", "/.git/config", "403"),
		accessLine("198.51.100.13", "/wp-admin/setup-config.php", "404"),
		accessLine("198.51.100.15", "/boaform/admin/formLogin", "404"),
		accessLine("198.51.100.17", "/cgi-bin/luci", "404"),
		accessLine("198.51.100.19", "/.aws/credentials", "404"),
		accessLine("198.51.100.21", "/backup.sql", "404"),
		accessLine("198.51.100.23", "/hnap1/", "404"),
		accessLine("198.51.100.25", "/autodiscover/autodiscover.xml", "404"),
		accessLine("198.51.100.27", "/wlwmanifest.xml", "404"),
		accessLine("198.51.100.29", "/shell.php", "404"),
	}
	v := classifyAlert(alertInput{Key: "watcher:x", Hits: len(lines), Lines: lines})

	if v.Class != alertRoutine {
		t.Fatalf("vulnerability scanning answered with 404/403 is background noise, got %q (%s)", v.Class, v.Reason)
	}
	if !strings.Contains(v.Reason, "404/403") {
		t.Errorf("the reason should say why it was ignored, got %q", v.Reason)
	}
}

// A refused scan stays routine even when ONE source is doing all of it. This
// reverses an earlier judgement: concentration used to win, on the theory that
// a single host walking a wordlist is doing something a scraper isn't. Real
// traffic disagreed — nearly every spike on these sites is exactly this shape,
// one scanner collecting 404s, and calling each one an incident is what buried
// the alerts that mattered. The server already said no; a person reading it
// does not make it more no.
func TestClassifyAlertRefusedScanIsRoutineEvenFromOneSource(t *testing.T) {
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, accessLine("45.155.205.99", fmt.Sprintf("/.env.%d", i), "404"))
	}
	v := classifyAlert(alertInput{Key: "watcher:x", Hits: 30, Lines: lines})

	if v.Class != alertRoutine {
		t.Fatalf("a refused scan should be routine however concentrated, got class=%q (%s)", v.Class, v.Reason)
	}
	if v.Concentrated {
		t.Error("routine traffic must not be marked blockable — that is what produces the block_ip proposals")
	}
	if hint := alertHint(v); strings.Contains(hint, "block_ip") {
		t.Errorf("no block proposal should be suggested for refused scanning, got %q", hint)
	}
}

// The same shape, but succeeding rather than being refused, IS an incident:
// requests getting through from one dominant source is a scrape or a
// brute-force, and blocking that source stops it.
func TestClassifyAlertConcentratedAndSucceedingIsIncident(t *testing.T) {
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, accessLine("45.155.205.99", "/wp-admin/admin-ajax.php", "200"))
	}
	v := classifyAlert(alertInput{Key: "watcher:x", Hits: 30, Lines: lines})

	if v.Class != alertIncident || !v.Concentrated {
		t.Fatalf("traffic getting through from one source should be a blockable incident, got class=%q concentrated=%v (%s)",
			v.Class, v.Concentrated, v.Reason)
	}
}

// Past a point volume alone decides. A refused flood is still a flood, and
// going quiet through one would be the opposite failure to the one this policy
// was written to fix.
func TestClassifyAlertFloodIsIncidentEvenWhenRefused(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, accessLine("45.155.205.99", "/.env", "404"))
	}
	v := classifyAlert(alertInput{Key: "watcher:x", Hits: alertFloodHits, Lines: lines})

	if v.Class != alertIncident {
		t.Fatalf("a flood should page whatever the status codes say, got class=%q (%s)", v.Class, v.Reason)
	}
	if !v.Concentrated {
		t.Error("a flood from one dominant source is still worth blocking")
	}
	// One under the threshold is back to routine, so the boundary is the constant.
	if v := classifyAlert(alertInput{Key: "watcher:x", Hits: alertFloodHits - 1, Lines: lines}); v.Class != alertRoutine {
		t.Errorf("just below the flood threshold should stay routine, got %q (%s)", v.Class, v.Reason)
	}
}

// The operator's own address must never be counted as an attack source. An
// admin clicking through a WordPress dashboard produces one source, many
// requests, in a burst — indistinguishable from a scrape by shape alone. This
// panel really did propose blocking its owner's home address for doing that.
func TestClassifyAlertIgnoresTheOperatorsOwnAddress(t *testing.T) {
	home := "5.186.58.205"
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, accessLine(home, "/wp-admin/admin-ajax.php", "200"))
	}
	in := alertInput{Key: "anomaly:log-rate", Hits: 40, Lines: lines, Exempt: map[string]bool{home: true}}
	v := classifyAlert(in)

	for _, s := range v.Sources {
		if s == home {
			t.Fatalf("the operator's own address must not appear as an attack source, got %v", v.Sources)
		}
	}
	if v.Concentrated {
		t.Error("with the only source exempted there is nothing to concentrate on")
	}
	if hint := alertHint(v); strings.Contains(hint, home) {
		t.Errorf("Kvasir must never be told to propose blocking the operator, got %q", hint)
	}
	// Without the exemption the same traffic reads as a blockable incident —
	// which is exactly the false positive being prevented.
	if bad := classifyAlert(alertInput{Key: "anomaly:log-rate", Hits: 40, Lines: lines}); !bad.Concentrated {
		t.Error("guard test is not proving anything: this traffic should look blockable without the exemption")
	}
}

func TestClassifyAlertLowVolumeIsRoutine(t *testing.T) {
	lines := []string{
		accessLine("203.0.113.5", "/wp-login.php", "200"),
		accessLine("203.0.113.5", "/wp-login.php", "200"),
	}
	v := classifyAlert(alertInput{Key: "watcher:x", Hits: 2, Lines: lines})

	if v.Class != alertRoutine {
		t.Fatalf("two failed logins is a normal day on the public internet, got %q (%s)", v.Class, v.Reason)
	}
}

// A watcher with no IPs at all — a PHP fatal, a game-server error — has no
// traffic shape to judge, so volume alone decides. This is the case that must
// not regress into silence: those watchers are an explicit admin opt-in.
func TestClassifyAlertNonTrafficWatcherStillPages(t *testing.T) {
	var lines []string
	for i := 0; i < 15; i++ {
		lines = append(lines, "PHP Fatal error:  Uncaught Error: Call to undefined function add_action()")
	}
	v := classifyAlert(alertInput{Key: "watcher:x", Hits: 15, Lines: lines})

	if v.Class != alertIncident {
		t.Fatalf("a repeated fatal error with no source IPs should still page, got %q (%s)", v.Class, v.Reason)
	}
	if v.Concentrated {
		t.Error("no sources means nothing to block")
	}
	if hint := alertHint(v); hint != "" {
		t.Errorf("no traffic shape means no hint to give, got %q", hint)
	}
}

// Private and Cloudflare addresses must never be counted as attack sources: the
// classifier shares checkBlockable with the firewall so it can't build a case
// around an address the panel would refuse to act on anyway.
func TestAnalyseSourcesSkipsAddressesWeWouldNeverBlock(t *testing.T) {
	lines := []string{
		accessLine("192.168.1.50", "/wp-login.php", "200"),
		accessLine("10.0.0.4", "/wp-login.php", "200"),
		accessLine("127.0.0.1", "/wp-login.php", "200"),
		accessLine("172.20.0.3", "/wp-login.php", "200"),
		accessLine("100.92.81.54", "/wp-login.php", "200"), // Tailscale CGNAT
		accessLine("8.8.8.8", "/wp-login.php", "200"),
	}
	sources, share := analyseSources(lines, nil)

	if len(sources) != 1 || sources[0] != "8.8.8.8" {
		t.Fatalf("only the public address should count as a source, got %v", sources)
	}
	if share != 1 {
		t.Errorf("the one counted source owns all the counted traffic, share = %v", share)
	}
}

func TestAnalyseSourcesOrdersByVolumeThenAddress(t *testing.T) {
	lines := []string{
		accessLine("203.0.113.9", "/a", "200"),
		accessLine("203.0.113.9", "/b", "200"),
		accessLine("203.0.113.9", "/c", "200"),
		accessLine("203.0.113.2", "/a", "200"),
		accessLine("198.51.100.4", "/a", "200"),
	}
	sources, share := analyseSources(lines, nil)

	if sources[0] != "203.0.113.9" {
		t.Fatalf("busiest source should come first, got %v", sources)
	}
	// Ties (203.0.113.2 and 198.51.100.4 have one each) sort by address, so the
	// recorded order is stable across runs rather than map-iteration order.
	if sources[1] != "198.51.100.4" || sources[2] != "203.0.113.2" {
		t.Errorf("ties should sort by address for a stable record, got %v", sources)
	}
	if want := 3.0 / 5.0; share != want {
		t.Errorf("share = %v, want %v", share, want)
	}
}

// One line mentioning the same address twice (client field plus an
// X-Forwarded-For, say) must not let that address out-vote the others.
func TestAnalyseSourcesCountsOneVotePerLine(t *testing.T) {
	lines := []string{
		`203.0.113.9 - - "POST /wp-login.php" 200 "-" "x-forwarded-for: 203.0.113.9"`,
		accessLine("198.51.100.4", "/a", "200"),
	}
	sources, share := analyseSources(lines, nil)

	if len(sources) != 2 {
		t.Fatalf("want both sources, got %v", sources)
	}
	if share != 0.5 {
		t.Errorf("the repeated address should hold half the votes, not two thirds; share = %v", share)
	}
}

func TestProbeShare(t *testing.T) {
	lines := []string{
		accessLine("203.0.113.1", "/.env", "404"),
		accessLine("203.0.113.2", "/", "200"),
		"",
		accessLine("203.0.113.3", "/about", "200"),
		accessLine("203.0.113.4", "/wp-admin/setup-config.php", "404"),
	}
	// Blank lines are ignored: 2 probes out of 4 real lines.
	if got := probeShare(lines); got != 0.5 {
		t.Errorf("probeShare = %v, want 0.5", got)
	}
	if got := probeShare(nil); got != 0 {
		t.Errorf("probeShare of nothing = %v, want 0", got)
	}
}

// A 200 answer is not a probe even on a path a scanner likes — that is a
// request that WORKED, which is the opposite of background noise.
func TestProbeShareIgnoresSuccessfulRequests(t *testing.T) {
	lines := []string{
		accessLine("203.0.113.1", "/index.php", "200"),
		accessLine("203.0.113.2", "/contact", "200"),
	}
	if got := probeShare(lines); got != 0 {
		t.Errorf("successful requests are not probes, probeShare = %v", got)
	}
}

// ---- volume detections (the log-rate anomaly) ----
//
// These are written from the alerts kw01 actually produced: with every watcher
// switched off it still paged 31 times in a day, all of them "traffic spike",
// none of them anything a person needed to see.

func TestVolumeSpikeWithNormalResponsesIsRoutine(t *testing.T) {
	// 204 log lines, ordinary visitors and a crawler pass — the real shape of
	// the alerts that woke the admin every 40 minutes.
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, accessLine("66.249.69.13", "/", "200"))
		lines = append(lines, accessLine("77.243.53.207", "/kontakt.html", "200"))
	}
	v := classifyAlert(alertInput{Key: "anomaly:log-rate", Hits: 204, Lines: lines, Volume: true})
	if v.Class != alertRoutine {
		t.Fatalf("a busy site with normal responses must not page, got %q (%s)", v.Class, v.Reason)
	}
}

func TestVolumeSpikeOfErrorsIsIncident(t *testing.T) {
	var lines []string
	for i := 0; i < 8; i++ {
		lines = append(lines, accessLine("203.0.113.9", "/", "200"))
	}
	for i := 0; i < 4; i++ {
		lines = append(lines, accessLine("203.0.113.9", "/", "502"))
	}
	v := classifyAlert(alertInput{Key: "anomaly:log-rate", Hits: 300, Lines: lines, Volume: true})
	if v.Class != alertIncident {
		t.Fatalf("a spike that is failing should page, got %q (%s)", v.Class, v.Reason)
	}
}

func TestVolumeFloodStillPages(t *testing.T) {
	lines := []string{accessLine("203.0.113.9", "/", "200")}
	v := classifyAlert(alertInput{Key: "anomaly:log-rate", Hits: 4000, Lines: lines, Volume: true})
	if v.Class != alertIncident {
		t.Fatalf("4000 lines in five minutes is a flood whatever the status codes, got %q", v.Class)
	}
}

// The same traffic reported by a WATCHER (which counts only matching lines) must
// keep paging exactly as before — the volume rules apply to volume detectors only.
func TestWatcherPathUnchangedByVolumeRules(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, accessLine("149.36.51.138", "/wp-login.php", "200"))
	}
	v := classifyAlert(alertInput{Key: "watcher:x", Hits: 20, Lines: lines})
	if v.Class != alertIncident || !v.Concentrated {
		t.Fatalf("watcher detections must be unaffected, got %q concentrated=%v", v.Class, v.Concentrated)
	}
}

// ---- source extraction ----

// The policy reported "100% of hits from 120.0.0.0 ... blocking it would stop
// this" for traffic whose only appearance of that string was Chrome's version.
func TestUserAgentVersionIsNotASource(t *testing.T) {
	line := `172.17.0.1 - - [04/Aug/2026:09:13:23 +0200] "GET /F0x.php HTTP/1.1" 404 536 "-" ` +
		`"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"`
	for _, ip := range lineSources(line) {
		if ip == "120.0.0.0" || ip == "537.36" {
			t.Fatalf("a version string was taken as a source address: %q", ip)
		}
	}
	if got := lineSources(line); len(got) != 1 || got[0] != "172.17.0.1" {
		t.Errorf("lineSources = %v, want just the client field", got)
	}
}

func TestApacheErrorLineClientIsFound(t *testing.T) {
	line := `[Tue Aug 04 09:13:23.336823 2026] [php:error] [pid 22:tid 22] ` +
		`[client 203.0.113.7:44368] script '/var/www/html/F0x.php' not found or unable to stat`
	got := lineSources(line)
	if len(got) != 1 || got[0] != "203.0.113.7" {
		t.Errorf("lineSources = %v, want the [client] address", got)
	}
}

// A non-web log has no client field; those still get the whole-line scan.
func TestNonWebLogStillYieldsSources(t *testing.T) {
	line := `[Server] Player Steve (198.51.100.4) failed authentication`
	got := lineSources(line)
	if len(got) != 1 || got[0] != "198.51.100.4" {
		t.Errorf("lineSources = %v, want the address from a plain log line", got)
	}
}

// Apache logs a missing PHP script as an error line AND an access-log 404. The
// error half didn't look like a probe, which dragged a pure scanner walk down to
// 60% and flipped it from routine to incident.
func TestApacheMissingScriptCountsAsProbe(t *testing.T) {
	lines := []string{
		`[Tue Aug 04 09:13:23.336823 2026] [php:error] [pid 22:tid 22] [client 172.17.0.1:44368] script '/var/www/html/F0x.php' not found or unable to stat`,
		accessLine("172.17.0.1", "/F0x.php", "404"),
		`[Tue Aug 04 09:13:23.362365 2026] [php:error] [pid 22:tid 22] [client 172.17.0.1:44368] script '/var/www/html/commonwp.php' not found or unable to stat`,
		accessLine("172.17.0.1", "/commonwp.php", "404"),
	}
	if got := probeShare(lines); got != 1 {
		t.Errorf("a scanner walk logged in both shapes is all probes, probeShare = %v", got)
	}
}

// ---- the two cases from Kristian's Discord, 4 Aug 11:23 and 11:35 ----

// 3dekoration.dk: a 404 spike from automated requests for PHP files that don't
// exist. Kvasir's own explanation said "the server is successfully handling the
// traffic with 404 responses" — and it paged anyway.
func TestScannerWalkOn404sIsRoutine(t *testing.T) {
	var lines []string
	for _, p := range []string{"/F0x.php", "/commonwp.php", "/wp-includes/js/tinymce/plugins/charmap/",
		"/.env", "/shell.php", "/wp-admin/setup-config.php", "/backup.zip", "/xmlrpc.php", "/old.php", "/db.php"} {
		lines = append(lines, accessLine("203.0.113.55", p, "404"))
	}
	v := classifyAlert(alertInput{Key: "anomaly:log-rate", Hits: 260, Lines: lines, Volume: true})
	if v.Class != alertRoutine {
		t.Fatalf("a scan the server answered with 404s must not page, got %q (%s)", v.Class, v.Reason)
	}
}

// lm-e.dk: a brute force from one address. As a VOLUME detection it is routine —
// but the rune's "Login brute force" watcher counts matches, not traffic, and
// must still page it. Losing that would be the one real risk of the volume fix.
func TestBruteForceRoutineByVolumeButPagedByWatcher(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, accessLine("20.218.182.51", "/wp-login.php", "200"))
	}
	if v := classifyAlert(alertInput{Key: "anomaly:log-rate", Hits: 240, Lines: lines, Volume: true}); v.Class != alertRoutine {
		t.Errorf("as a traffic spike this is routine, got %q (%s)", v.Class, v.Reason)
	}
	v := classifyAlert(alertInput{Key: "watcher:login", Hits: 20, Lines: lines})
	if v.Class != alertIncident || !v.Concentrated {
		t.Fatalf("the brute-force WATCHER must still page and stay blockable, got %q concentrated=%v", v.Class, v.Concentrated)
	}
	if len(v.Sources) != 1 || v.Sources[0] != "20.218.182.51" {
		t.Errorf("sources = %v, want the attacker", v.Sources)
	}
}
