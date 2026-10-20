package cloudflare

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeCF is a small Cloudflare: two zones, a DNS record store per zone, and a
// tunnel config. It records every write so a test can assert what changed.
type fakeCF struct {
	mu        sync.Mutex
	zonesDown bool
	zoneGets  int
	records   map[string][]map[string]any // zone id -> records
	ingress   []any
	writes    []string // "METHOD path"
}

func newFakeCF() *fakeCF {
	return &fakeCF{records: map[string][]map[string]any{}}
}

func (f *fakeCF) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := func(v any) {
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": v}) //nolint:errcheck
	}
	if r.Method != http.MethodGet {
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
	}
	switch {
	case r.URL.Path == "/zones":
		f.zoneGets++
		if f.zonesDown {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("upstream hiccup")) //nolint:errcheck
			return
		}
		ok([]map[string]any{
			{"id": "zhome", "name": "panel.example"},
			{"id": "zother", "name": "other.example"},
		})
	case strings.HasSuffix(r.URL.Path, "/configurations"):
		if r.Method == http.MethodPut {
			var body struct {
				Config map[string]any `json:"config"`
			}
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
			f.ingress, _ = body.Config["ingress"].([]any)
			ok(map[string]any{})
			return
		}
		ok(map[string]any{"config": map[string]any{"ingress": f.ingress}})
	case strings.Contains(r.URL.Path, "/dns_records"):
		zone := strings.Split(strings.TrimPrefix(r.URL.Path, "/zones/"), "/")[0]
		switch r.Method {
		case http.MethodGet:
			out := []map[string]any{}
			for _, rec := range f.records[zone] {
				if strings.EqualFold(rec["name"].(string), r.URL.Query().Get("name")) &&
					strings.EqualFold(rec["type"].(string), r.URL.Query().Get("type")) {
					out = append(out, rec)
				}
			}
			ok(out)
		case http.MethodDelete:
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			kept := f.records[zone][:0]
			for _, rec := range f.records[zone] {
				if rec["id"] != id {
					kept = append(kept, rec)
				}
			}
			f.records[zone] = kept
			ok(map[string]any{})
		default:
			ok(map[string]any{})
		}
	default:
		ok(map[string]any{})
	}
}

func newTestClient(t *testing.T, f *fakeCF) (*Client, *[]string) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New("tok", "acct", "zhome", "tun")
	c.baseURL = srv.URL
	var lines []string
	c.Logf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	return c, &lines
}

// The fault this replaces: on a failed zone lookup the client kept the zone the
// previous hostname had left it in, so the next DNS call ran in the wrong zone.
// A failed lookup must leave NO zone selected, so that a DNS call refuses.
func TestUseZoneForFailureSelectsNoZone(t *testing.T) {
	f := newFakeCF()
	c, _ := newTestClient(t, f)
	if err := c.UseZoneFor("app.other.example"); err != nil {
		t.Fatalf("UseZoneFor: %v", err)
	}
	if c.ZoneID() != "zother" {
		t.Fatalf("zone = %q, want zother", c.ZoneID())
	}

	f.mu.Lock()
	f.zonesDown = true
	f.mu.Unlock()
	c2, _ := newTestClient(t, f)
	c2.zoneID = "zother" // as if a previous route had pointed it there
	if err := c2.UseZoneFor("x.panel.example"); err == nil {
		t.Fatal("UseZoneFor must report a failed zone lookup")
	}
	if c2.ZoneID() != "" {
		t.Fatalf("zone after a failed lookup = %q, want none selected", c2.ZoneID())
	}
	if err := c2.RemoveDNS("x.panel.example"); err == nil {
		t.Fatal("RemoveDNS with no zone selected must refuse, not act in some zone")
	}
}

func TestUseZoneForFallsBackToHomeAndListsZonesOnce(t *testing.T) {
	f := newFakeCF()
	c, _ := newTestClient(t, f)
	for host, want := range map[string]string{
		"a.other.example":   "zother",
		"b.panel.example":   "zhome",
		"c.unowned.example": "zhome", // the account owns no matching zone
	} {
		if err := c.UseZoneFor(host); err != nil {
			t.Fatalf("UseZoneFor(%s): %v", host, err)
		}
		if c.ZoneID() != want {
			t.Errorf("UseZoneFor(%s) = %q, want %q", host, c.ZoneID(), want)
		}
	}
	if f.zoneGets != 1 {
		t.Errorf("listed the zones %d times for three hostnames, want 1", f.zoneGets)
	}
}

// Every write is logged with its hostname and zone, and a no-op writes nothing
// and logs nothing — otherwise every start would log every name and the one
// line that matters would drown.
func TestDNSChangesAreLoggedAndNoOpsAreSilent(t *testing.T) {
	f := newFakeCF()
	c, lines := newTestClient(t, f)
	c.zoneID = "zother"

	if err := c.EnsureDNS("app.other.example"); err != nil {
		t.Fatalf("EnsureDNS: %v", err)
	}
	if len(*lines) != 1 || !strings.Contains((*lines)[0], "created DNS record app.other.example") ||
		!strings.Contains((*lines)[0], "zother") {
		t.Fatalf("create log = %q", *lines)
	}

	f.mu.Lock()
	f.records["zother"] = []map[string]any{{"id": "r1", "type": "CNAME", "name": "app.other.example",
		"content": "tun.cfargotunnel.com", "proxied": true}}
	f.writes = nil
	f.mu.Unlock()
	*lines = nil
	if err := c.EnsureDNS("app.other.example"); err != nil {
		t.Fatalf("EnsureDNS: %v", err)
	}
	if len(f.writes) != 0 || len(*lines) != 0 {
		t.Fatalf("an already-correct record must cost no write and no log line; writes=%v log=%v", f.writes, *lines)
	}

	if err := c.RemoveDNS("app.other.example"); err != nil {
		t.Fatalf("RemoveDNS: %v", err)
	}
	if len(*lines) != 1 || !strings.Contains((*lines)[0], "deleted DNS record app.other.example (zone zother)") {
		t.Fatalf("delete log = %q", *lines)
	}
}

func TestIngressChangesAreLoggedAndNoOpsAreSilent(t *testing.T) {
	f := newFakeCF()
	c, lines := newTestClient(t, f)

	if err := c.UpsertHostname("a.panel.example", "http://10.0.0.1:80"); err != nil {
		t.Fatal(err)
	}
	if err := c.UpsertHostname("a.panel.example", "http://10.0.0.1:80"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveHostname("never-there.panel.example"); err != nil {
		t.Fatal(err)
	}
	if got := len(f.writes); got != 1 {
		t.Errorf("tunnel config PUTs = %d (%v), want 1: a repeat and an absent removal must not write", got, f.writes)
	}
	if len(*lines) != 1 || !strings.Contains((*lines)[0], "added tunnel route a.panel.example") {
		t.Fatalf("log = %q", *lines)
	}

	if err := c.RemoveHostname("a.panel.example"); err != nil {
		t.Fatal(err)
	}
	if len(*lines) != 2 || !strings.Contains((*lines)[1], "removed tunnel route a.panel.example") {
		t.Fatalf("log = %q", *lines)
	}
	rules, _ := json.Marshal(f.ingress)
	if strings.Contains(string(rules), "a.panel.example") || !strings.Contains(string(rules), "http_status:404") {
		t.Errorf("ingress after removal = %s, want only the catch-all", rules)
	}
}
