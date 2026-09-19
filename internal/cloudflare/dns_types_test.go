package cloudflare

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The bug this guards, in one sentence: a name in Cloudflare is a SET of
// records, and the panel used to fetch that set by name alone, take whichever
// came back first, and PUT a CNAME over its id.
//
// At a zone apex that set contains the domain's MX and TXT. On 2026-09-19 it
// converted live mail records into tunnel CNAMEs — MX on two domains, SPF on a
// third. Nothing errored, because overwriting the wrong record is a perfectly
// successful API call.
//
// The fake below returns MX FIRST for every unfiltered query, which is what
// makes this a regression test rather than a hopeful one: with the old code it
// fails, and it fails by destroying the MX exactly as production did.
func TestEnsureDNSNeverTouchesMailRecords(t *testing.T) {
	var mu sync.Mutex
	records := []map[string]any{
		{"id": "mx1", "type": "MX", "name": "example.com", "content": "mail.example.com"},
		{"id": "txt1", "type": "TXT", "name": "example.com", "content": "v=spf1 mx ~all"},
		{"id": "a1", "type": "A", "name": "example.com", "content": "203.0.113.9"},
	}
	var puts []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet:
			want := r.URL.Query().Get("type")
			out := []map[string]any{}
			for _, rec := range records {
				if want == "" || strings.EqualFold(rec["type"].(string), want) {
					out = append(out, rec)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"success": true, "result": out}) //nolint:errcheck
		case r.Method == http.MethodPut:
			puts = append(puts, strings.TrimPrefix(r.URL.Path, "/zones/z/dns_records/"))
			json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{}}) //nolint:errcheck
		default: // POST
			json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{}}) //nolint:errcheck
		}
	}))
	defer srv.Close()

	c := New("tok", "acct", "z", "tun")
	c.baseURL = srv.URL
	if err := c.EnsureDNS("example.com"); err != nil {
		t.Fatalf("EnsureDNS: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, id := range puts {
		if id == "mx1" {
			t.Fatal("the MX record was overwritten — this is the outage that deleted live mail")
		}
		if id == "txt1" {
			t.Fatal("the SPF record was overwritten")
		}
	}
	if len(puts) != 1 || puts[0] != "a1" {
		t.Errorf("expected the apex A record to be taken over and nothing else, got %v", puts)
	}
}

// Removal had the mirror of the same fault: asking without a type could return
// the MX, whose content never equals the tunnel target, so RemoveDNS decided
// the record was "not ours" and silently removed nothing — leaving the CNAME
// behind while reporting success.
func TestRemoveDNSFindsTheCNAMEPastAnMX(t *testing.T) {
	var deleted []string
	records := []map[string]any{
		{"id": "mx1", "type": "MX", "name": "example.com", "content": "mail.example.com"},
		{"id": "cn1", "type": "CNAME", "name": "example.com", "content": "tun.cfargotunnel.com"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/zones/z/dns_records/"))
			json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{}}) //nolint:errcheck
			return
		}
		want := r.URL.Query().Get("type")
		out := []map[string]any{}
		for _, rec := range records {
			if want == "" || strings.EqualFold(rec["type"].(string), want) {
				out = append(out, rec)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": out}) //nolint:errcheck
	}))
	defer srv.Close()

	c := New("tok", "acct", "z", "tun")
	c.baseURL = srv.URL
	if err := c.RemoveDNS("example.com"); err != nil {
		t.Fatalf("RemoveDNS: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "cn1" {
		t.Errorf("expected only the tunnel CNAME to be deleted, got %v", deleted)
	}
}

// A type must always be named. Defaulting to "any record" is how the original
// bug reads at the call site: innocuous.
func TestFindDNSRefusesWithoutAType(t *testing.T) {
	c := New("tok", "acct", "z", "tun")
	if _, err := c.findDNS("example.com"); err == nil {
		t.Error("findDNS with no type must refuse rather than match anything")
	}
}
