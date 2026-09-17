package cloudflare

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// The tunnel's ingress list is one shared document edited by read-modify-write.
// Without a lock, two concurrent removals both read the same starting state and
// the second PUT undoes the first — silently. That is how, on 2026-09-16, eleven
// DNS records were deleted while only seven ingress rules were: the DNS deletes
// are independent per-record calls and cannot lose, the ingress edits could.
//
// This test reproduces the shape: several goroutines each remove their own
// hostname from a server that models the real read-modify-write, with a
// deliberate gap between GET and PUT to widen the window.
func TestConcurrentRemovalsDoNotLoseEachOther(t *testing.T) {
	const n = 8

	var mu sync.Mutex // guards the fake server's own state, not the client
	rules := []map[string]any{}
	for i := 0; i < n; i++ {
		rules = append(rules, map[string]any{"hostname": fmt.Sprintf("h%d.example.com", i), "service": "http://x"})
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			mu.Lock()
			snapshot := append([]map[string]any{}, rules...)
			mu.Unlock()
			ing := make([]any, 0, len(snapshot)+1)
			for _, x := range snapshot {
				ing = append(ing, x)
			}
			ing = append(ing, map[string]any{"service": "http_status:404"})
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"success": true,
				"result":  map[string]any{"config": map[string]any{"ingress": ing}},
			})
		case http.MethodPut:
			var body struct {
				Config struct {
					Ingress []map[string]any `json:"ingress"`
				} `json:"config"`
			}
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
			kept := []map[string]any{}
			for _, x := range body.Config.Ingress {
				if h, _ := x["hostname"].(string); h != "" {
					kept = append(kept, x)
				}
			}
			mu.Lock()
			rules = kept
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{}}) //nolint:errcheck
		}
	}))
	defer srv.Close()

	c := New("tok", "acct", "zone", "tunnel-race-test")
	c.baseURL = srv.URL

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := c.RemoveHostname(fmt.Sprintf("h%d.example.com", i)); err != nil {
				t.Errorf("remove h%d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	mu.Lock()
	left := len(rules)
	remaining := rules
	mu.Unlock()
	if left != 0 {
		t.Errorf("after removing all %d hostnames, %d survived — a write was lost: %v", n, left, remaining)
	}
}
