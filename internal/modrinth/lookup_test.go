package modrinth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// LookupByHashes posts the hashes with the sha512 algorithm and maps the response
// (hash → version) back. An empty input skips the call entirely.
func TestLookupByHashes(t *testing.T) {
	var gotBody map[string]any
	withFake(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Write([]byte(`{"abc":{"id":"v1","project_id":"P","version_number":"1.0"}}`))
	})
	out, err := LookupByHashes(context.Background(), []string{"abc"})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["algorithm"] != "sha512" {
		t.Errorf("algorithm = %v, want sha512", gotBody["algorithm"])
	}
	if v, ok := out["abc"]; !ok || v.ProjectID != "P" {
		t.Errorf("out = %+v", out)
	}

	// empty input makes no request
	out, err = LookupByHashes(context.Background(), nil)
	if err != nil || len(out) != 0 {
		t.Errorf("empty lookup should no-op, got %v %v", out, err)
	}
}

// LatestByHashes needs a concrete game version — without one it can't pin an
// update and returns empty rather than a misleading answer.
func TestLatestByHashesNeedsVersion(t *testing.T) {
	called := false
	withFake(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{}`))
	})
	out, err := LatestByHashes(context.Background(), []string{"abc"}, []string{"fabric"}, "")
	if err != nil || len(out) != 0 || called {
		t.Errorf("blank version should skip the call; called=%v out=%v", called, out)
	}
}

func TestGetProjectsKeysByID(t *testing.T) {
	withFake(t, func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query().Get("ids"); q != `["AANobbMI"]` {
			t.Errorf("ids query = %q", q)
		}
		w.Write([]byte(`[{"id":"AANobbMI","slug":"sodium","title":"Sodium"}]`))
	})
	out, err := GetProjects(context.Background(), []string{"AANobbMI"})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := out["AANobbMI"]; !ok || p.Title != "Sodium" {
		t.Errorf("out = %+v", out)
	}
}
