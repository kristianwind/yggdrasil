package modrinth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func withFake(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	old := baseURL
	baseURL = srv.URL
	t.Cleanup(func() { baseURL = old })
}

// Search must send a descriptive User-Agent (Modrinth 403s requests without one)
// and encode the loader/version facets as an AND of OR-groups.
func TestSearchSendsUAAndFacets(t *testing.T) {
	var gotUA, gotFacets string
	withFake(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotFacets = r.URL.Query().Get("facets")
		w.Write([]byte(`{"hits":[{"slug":"sodium","title":"Sodium","project_id":"AANobbMI"}]}`))
	})
	hits, err := Search(context.Background(), "sodium", []string{"paper", "spigot"}, "1.20.1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if gotUA == "" {
		t.Error("no User-Agent sent — Modrinth will 403")
	}
	// paper OR spigot in one group, AND the version in another.
	want := `[["categories:paper","categories:spigot"],["versions:1.20.1"]]`
	if gotFacets != want {
		t.Errorf("facets = %q, want %q", gotFacets, want)
	}
	if len(hits) != 1 || hits[0].Slug != "sodium" {
		t.Errorf("hits = %+v", hits)
	}
}

// With no version pinned, the facet must omit the version group entirely (not
// send an empty one) so results aren't filtered to nothing.
func TestSearchOmitsBlankVersion(t *testing.T) {
	var gotFacets string
	withFake(t, func(w http.ResponseWriter, r *http.Request) {
		gotFacets = r.URL.Query().Get("facets")
		w.Write([]byte(`{"hits":[]}`))
	})
	Search(context.Background(), "x", []string{"fabric"}, "", 10)
	if gotFacets != `[["categories:fabric"]]` {
		t.Errorf("facets = %q, want only the loader group", gotFacets)
	}
}

// ResolveVersion picks the newest published version and returns ErrNotFound when
// the API returns an empty list (no build for that loader/version).
func TestResolveVersionPicksNewest(t *testing.T) {
	withFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"id":"old","version_number":"1.0","date_published":"2023-01-01T00:00:00Z","files":[{"url":"u1","filename":"a.jar","primary":true,"hashes":{"sha512":"h1"}}]},
			{"id":"new","version_number":"2.0","date_published":"2024-06-01T00:00:00Z","files":[{"url":"u2","filename":"b.jar","primary":true,"hashes":{"sha512":"h2"}}]}
		]`))
	})
	v, err := ResolveVersion(context.Background(), "sodium", []string{"fabric"}, "1.20.1")
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "new" {
		t.Errorf("picked %q, want the newest 'new'", v.ID)
	}
	f, err := v.PrimaryFile()
	if err != nil || f.Filename != "b.jar" || f.Hashes.SHA512 != "h2" {
		t.Errorf("primary file = %+v (err %v)", f, err)
	}
}

func TestResolveVersionEmptyIsNotFound(t *testing.T) {
	withFake(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	if _, err := ResolveVersion(context.Background(), "x", []string{"fabric"}, "1.99"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// The loader/version must reach the version endpoint as JSON-array query params.
func TestResolveVersionQueryEncoding(t *testing.T) {
	var q url.Values
	withFake(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		w.Write([]byte(`[{"id":"v","files":[{"url":"u","filename":"f.jar","primary":true}]}]`))
	})
	ResolveVersion(context.Background(), "sodium", []string{"fabric"}, "1.20.1")
	if q.Get("loaders") != `["fabric"]` {
		t.Errorf("loaders = %q", q.Get("loaders"))
	}
	if q.Get("game_versions") != `["1.20.1"]` {
		t.Errorf("game_versions = %q", q.Get("game_versions"))
	}
}
