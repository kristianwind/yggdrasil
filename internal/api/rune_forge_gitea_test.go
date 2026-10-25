package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestNormalizeForgeHost(t *testing.T) {
	cases := []struct {
		in, want string
		bad      bool
	}{
		{"", "", false},
		{"https://gitea.example.com", "https://gitea.example.com", false},
		{"https://Gitea.Example.com/", "https://gitea.example.com", false},
		{"gitea.example.com", "https://gitea.example.com", false},
		{"https://example.com/git/", "https://example.com/git", false},
		{"https://gitea.example.com:3000", "https://gitea.example.com:3000", false},
		{"https://github.com", "", false}, // GitHub is the default, not a host
		{"http://gitea.example.com", "", true},
		{"https://user:pass@gitea.example.com", "", true},
		{"https://gitea.example.com/?x=1", "", true},
		{"https://", "", true},
	}
	for _, c := range cases {
		got, err := normalizeForgeHost(c.in)
		if c.bad != (err != nil) || got != c.want {
			t.Errorf("normalizeForgeHost(%q) = %q, %v; want %q, bad=%v", c.in, got, err, c.want, c.bad)
		}
	}
}

// What the panel builds, it must be able to read back — and nothing else.
func TestGiteaRawSourceRoundTrip(t *testing.T) {
	hosts := []string{"https://gitea.example.com", "https://example.com/git"}
	for _, c := range []struct{ host, repo, file, ref, wantPath string }{
		{"https://gitea.example.com", "kw/cellarius", "rune/cellarius.yaml", "main", "rune"},
		{"https://gitea.example.com", "kw/runes", "top.yaml", "v1.2", ""},
		{"https://example.com/git", "team/apps", "apps/web/site.yaml", "release/2026", "apps/web"},
	} {
		raw := giteaRawURL(c.host, c.repo, c.file, c.ref)
		h, repo, path, ref, ok := parseGiteaRawSource(raw, hosts)
		if !ok || h != c.host || repo != c.repo || path != c.wantPath || ref != c.ref {
			t.Errorf("round trip of %s = %q %q %q %q ok=%v", raw, h, repo, path, ref, ok)
		}
	}
	for _, raw := range []string{
		"https://evil.example.net/api/v1/repos/kw/x/raw/a.yaml?ref=main",  // host nobody saved
		"https://gitea.example.com.evil.net/api/v1/repos/kw/x/raw/a.yaml", // prefix trick
		"https://gitea.example.com/kw/x/raw/branch/main/a.yaml",           // Gitea's own download_url shape
		"http://gitea.example.com/api/v1/repos/kw/x/raw/a.yaml?ref=main",  // not https
	} {
		if _, _, _, _, ok := parseGiteaRawSource(raw, hosts); ok {
			t.Errorf("parseGiteaRawSource accepted %s", raw)
		}
	}
}

// The panel-wide token is GitHub's. A Gitea repository gets its own token or
// none — never that one.
func TestRuneTokenNeverLeavesGithub(t *testing.T) {
	ctx := context.Background()
	s := transferTestServer(t, "gitea-rune-token-key-0123456789a")
	globalEnc, _ := s.cipher.Encrypt("github-token")
	s.setSetting(ctx, "github_token", globalEnc)
	repoEnc, _ := s.storeRepoToken("gitea-token")
	s.db.Exec("INSERT INTO rune_repos (id, name, host, repo, path, ref, token_enc) VALUES ('g1','G','https://gitea.example.com','kw/private','rune','main',?)", repoEnc)
	s.db.Exec("INSERT INTO rune_repos (id, name, host, repo, path, ref, token_enc) VALUES ('g2','G2','https://gitea.example.com','kw/public','rune','main','')")
	// The same owner/repo on GitHub must not pick up the Gitea row's token either.
	cases := []struct{ host, repo, want string }{
		{"https://gitea.example.com", "kw/private", "gitea-token"},
		{"https://gitea.example.com", "kw/public", ""},
		{"https://gitea.example.com", "kw/unsaved", ""},
		{"", "kw/private", "github-token"},
	}
	for _, c := range cases {
		if got := s.effectiveRuneToken(ctx, c.host, c.repo, "rune", "main"); got != c.want {
			t.Errorf("effectiveRuneToken(%q, %q) = %q, want %q", c.host, c.repo, got, c.want)
		}
	}
}

// fakeGitea answers the three calls the panel makes, for one private repository.
type fakeGitea struct {
	mu    sync.Mutex
	auths []string
	paths []string
}

func (f *fakeGitea) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	f.paths = append(f.paths, r.URL.Path)
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "token gitea-token" {
		http.NotFound(w, r) // what Gitea does for a private repo you cannot see
		return
	}
	switch r.URL.Path {
	case "/api/v1/repos/kw/private/contents/runes":
		json.NewEncoder(w).Encode([]map[string]string{ //nolint:errcheck
			{"name": "notes.yaml", "path": "runes/notes.yaml", "type": "file",
				// What a misconfigured ROOT_URL hands back; the panel must not use it.
				"download_url": "http://10.0.0.9:3000/kw/private/raw/branch/main/runes/notes.yaml"},
			{"name": "apps", "path": "runes/apps", "type": "dir"},
			{"name": "README.md", "path": "runes/README.md", "type": "file"},
		})
	case "/api/v1/repos/kw/private/contents/runes/apps":
		json.NewEncoder(w).Encode([]map[string]string{ //nolint:errcheck
			{"name": "wiki.yml", "path": "runes/apps/wiki.yml", "type": "file"},
		})
	case "/api/v1/repos/kw/private/raw/runes/notes.yaml":
		w.Write([]byte("gameskill:\n  id: acme-notes\n  name: Acme Notes\n  version: 3\n  docker:\n    image: ghcr.io/acme/notes:latest\n  startup:\n    command: exec /app/notes\n")) //nolint:errcheck
	case "/api/v1/repos/kw/private/raw/runes/apps/wiki.yml":
		w.Write([]byte("gameskill:\n  id: acme-wiki\n  name: Acme Wiki\n  docker:\n    image: ghcr.io/acme/wiki:latest\n  startup:\n    command: exec /app/wiki\n")) //nolint:errcheck
	default:
		http.NotFound(w, r)
	}
}

func TestGiteaRuneRepoListAndInstall(t *testing.T) {
	ctx := context.Background()
	fg := &fakeGitea{}
	srv := httptest.NewTLSServer(fg)
	defer srv.Close()
	old := http.DefaultClient
	http.DefaultClient = srv.Client()
	t.Cleanup(func() { http.DefaultClient = old })

	s := transferTestServer(t, "gitea-rune-list-key-0123456789ab")
	globalEnc, _ := s.cipher.Encrypt("github-token")
	s.setSetting(ctx, "github_token", globalEnc)

	list := func(host string) *httptest.ResponseRecorder {
		q := url.Values{"host": {host}, "repo": {"kw/private"}, "path": {"runes"}, "ref": {"main"}, "refresh": {"1"}}
		rec := httptest.NewRecorder()
		s.handleGithubRunes(rec, httptest.NewRequest("GET", "/api/gameskills/github?"+q.Encode(), nil))
		return rec
	}

	// Before any repository names the host, the panel does not contact it.
	if rec := list(srv.URL); rec.Code != http.StatusBadRequest || len(fg.paths) != 0 {
		t.Fatalf("unsaved host: %d, %d requests made; want 400 and none", rec.Code, len(fg.paths))
	}

	repoEnc, _ := s.storeRepoToken("gitea-token")
	s.db.Exec("INSERT INTO rune_repos (id, name, host, repo, path, ref, token_enc) VALUES ('g1','Mine',?,'kw/private','runes','main',?)", srv.URL, repoEnc)

	rec := list(srv.URL)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Host  string   `json:"host"`
		Runes []ghRune `json:"runes"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body) //nolint:errcheck
	if body.Host != srv.URL || len(body.Runes) != 2 {
		t.Fatalf("listing = %+v, want 2 runes from %s", body, srv.URL)
	}
	byID := map[string]ghRune{}
	for _, g := range body.Runes {
		if g.ParseError != "" {
			t.Errorf("%s: %s", g.Filename, g.ParseError)
		}
		if !strings.HasPrefix(g.DownloadURL, srv.URL+"/api/v1/repos/kw/private/raw/") {
			t.Errorf("%s: download url %s is not one the panel built", g.Filename, g.DownloadURL)
		}
		byID[g.ID] = g
	}
	if byID["acme-notes"].Version != 3 || byID["acme-wiki"].Name != "Acme Wiki" {
		t.Errorf("parsed runes = %+v", byID)
	}
	fg.mu.Lock()
	for i, a := range fg.auths {
		if a != "token gitea-token" {
			t.Errorf("request %s carried %q — only the repository's own token may reach Gitea", fg.paths[i], a)
		}
	}
	fg.mu.Unlock()

	// Install from the listing: stored, and remembers which server it came from.
	in, _ := json.Marshal(map[string]string{"download_url": byID["acme-notes"].DownloadURL})
	rec = httptest.NewRecorder()
	s.handleInstallGithubRune(rec, httptest.NewRequest("POST", "/", bytes.NewReader(in)))
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body)
	}
	var host, repo, path, ref string
	s.db.QueryRow("SELECT source_host, source_repo, source_path, source_ref FROM gameskills WHERE id='acme-notes'").Scan(&host, &repo, &path, &ref) //nolint:errcheck
	if host != srv.URL || repo != "kw/private" || path != "runes" || ref != "main" {
		t.Errorf("recorded source = %q %q %q %q", host, repo, path, ref)
	}

	// The update check reads the same server: bump nothing, expect no update and no note.
	rec = httptest.NewRecorder()
	s.handleRuneUpdates(rec, httptest.NewRequest("GET", "/", nil))
	var upd runeUpdatesResponse
	json.Unmarshal(rec.Body.Bytes(), &upd) //nolint:errcheck
	for _, u := range upd.Updates {
		if u.ID == "acme-notes" {
			t.Errorf("acme-notes reported as an update when the versions match: %+v", u)
		}
	}
	if strings.Contains(upd.Note, srv.URL) {
		t.Errorf("update check could not reach the Gitea source: %s", upd.Note)
	}

	// A download URL on a host nobody saved is refused, and never fetched.
	before := len(fg.paths)
	in, _ = json.Marshal(map[string]string{"download_url": "https://other.example.net/api/v1/repos/kw/private/raw/runes/notes.yaml?ref=main"})
	rec = httptest.NewRecorder()
	s.handleInstallGithubRune(rec, httptest.NewRequest("POST", "/", bytes.NewReader(in)))
	if rec.Code == http.StatusCreated || len(fg.paths) != before {
		t.Errorf("install from an unsaved host: %d, %d new requests", rec.Code, len(fg.paths)-before)
	}
}
