package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// Rune repositories on a self-hosted Gitea (or Forgejo, which speaks the same
// API), public or private.
//
// A rune repository is GitHub unless it names a host. With a host, listing and
// installing go through that server's /api/v1 instead, authenticated with the
// token saved for that repository — never with the panel-wide GitHub token, which
// is GitHub's credential and has no business reaching another server.
//
// Two rules keep a configurable host from turning the rune browser into a fetch
// proxy:
//
//   - A host is only ever contacted when a saved rune repository names it. The
//     listing and install endpoints take the host from the request, so without
//     this anyone with admin rights could point the panel at any address the box
//     can reach and read back what answered.
//   - Every URL the panel fetches is one it built itself from that host, never one
//     a server handed back. Gitea builds its download_url from its own ROOT_URL,
//     which can name an address clients should not be fetching from at all (it
//     did on our own instance, for months: plain HTTP to a raw IP).

// normalizeForgeHost turns what an admin typed into the canonical base URL a
// repository row stores: scheme and host (plus a sub-path, for a Gitea served
// under one), no trailing slash, no query. "" and GitHub itself mean GitHub.
//
// HTTPS only. The token travels in a header on every request, and a self-hosted
// forge is exactly the kind of server that gets set up on plain HTTP "for now".
func normalizeForgeHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("server must be an address like https://gitea.example.com")
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("server must use https — the repository token is sent with every request")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("server must be a plain address like https://gitea.example.com, without a login, query or #fragment")
	}
	host := strings.ToLower(u.Host)
	if host == "github.com" || host == "www.github.com" || host == "api.github.com" {
		return "", nil
	}
	return "https://" + host + strings.TrimRight(u.EscapedPath(), "/"), nil
}

// forgeHostKnown reports whether a saved rune repository names this host.
func (s *Server) forgeHostKnown(ctx context.Context, host string) bool {
	if host == "" {
		return true // GitHub, gated by ghAllowedHosts instead
	}
	var n int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM rune_repos WHERE host=?", host).Scan(&n) //nolint:errcheck
	return n > 0
}

// giteaHTTP issues a request to a Gitea API URL. The token is attached only when
// the URL is under host's own API, so a mismatched URL can never carry it away.
func giteaHTTP(ctx context.Context, host, rawurl, token string) (*http.Response, error) {
	if !strings.HasPrefix(rawurl, host+"/api/v1/") {
		return nil, fmt.Errorf("url not under %s/api/v1", host)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", rawurl, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yggdrasil-rune-browser")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	return http.DefaultClient.Do(req)
}

// giteaPathEscape escapes each segment of a repository path, keeping the slashes.
func giteaPathEscape(p string) string {
	seg := strings.Split(strings.Trim(p, "/"), "/")
	for i := range seg {
		seg[i] = url.PathEscape(seg[i])
	}
	return strings.Join(seg, "/")
}

// giteaRawURL is the API's raw-file address for one file. This, not the listing's
// download_url, is what the panel fetches and what an installed rune records.
func giteaRawURL(host, repo, file, ref string) string {
	return fmt.Sprintf("%s/api/v1/repos/%s/raw/%s?ref=%s", host, repo, giteaPathEscape(file), url.QueryEscape(ref))
}

// parseGiteaRawSource recovers repo, folder, ref and file from a URL giteaRawURL
// built, for a host the panel knows. ok is false for anything else.
func parseGiteaRawSource(raw string, hosts []string) (host, repo, path, ref string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return "", "", "", "", false
	}
	for _, h := range hosts {
		prefix := h + "/api/v1/repos/"
		if !strings.HasPrefix(raw, prefix) {
			continue
		}
		hu, err := url.Parse(h)
		if err != nil || !strings.EqualFold(hu.Host, u.Host) {
			continue
		}
		rest := strings.TrimPrefix(u.Path, strings.TrimRight(hu.Path, "/")+"/api/v1/repos/")
		seg := strings.Split(rest, "/")
		// owner, repo, "raw", file...
		if len(seg) < 4 || seg[2] != "raw" {
			return "", "", "", "", false
		}
		repo = seg[0] + "/" + seg[1]
		if len(seg) > 4 {
			path = strings.Join(seg[3:len(seg)-1], "/")
		}
		ref = u.Query().Get("ref")
		if ref == "" {
			ref = defaultRuneRef
		}
		if !ghRepoRe.MatchString(repo) || !ghRefRe.MatchString(ref) {
			return "", "", "", "", false
		}
		return h, repo, path, ref, true
	}
	return "", "", "", "", false
}

// forgeHosts lists the hosts saved rune repositories name.
func (s *Server) forgeHosts(ctx context.Context) []string {
	var out []string
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT host FROM rune_repos WHERE host<>''")
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if rows.Scan(&h) == nil {
			out = append(out, h)
		}
	}
	return out
}

type giteaEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"` // "file" | "dir"
}

func giteaListDir(ctx context.Context, host, repo, path, ref, token string) ([]giteaEntry, error) {
	listURL := fmt.Sprintf("%s/api/v1/repos/%s/contents/%s?ref=%s", host, repo, giteaPathEscape(path), url.QueryEscape(ref))
	resp, err := giteaHTTP(ctx, host, listURL, token)
	if err != nil {
		return nil, fmt.Errorf("%s unreachable: %w", host, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		// Gitea, like GitHub, answers 404 for a private repository the caller
		// cannot see — so a miss without a token is usually the token.
		if token == "" {
			return nil, fmt.Errorf("not found: %s/%s@%s on %s — if the repository is private, save a token for it", repo, path, ref, host)
		}
		return nil, fmt.Errorf("not found: %s/%s@%s on %s — check the repository, folder and branch, and that the token can read this repository", repo, path, ref, host)
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("%s refused the token (%d) — it needs read access to repositories (scope read:repository)", host, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %d", host, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var listing []giteaEntry
	if err := json.Unmarshal(body, &listing); err != nil {
		// A path that names a file answers with one object, not a list.
		return nil, fmt.Errorf("%s/%s on %s is not a folder", repo, path, host)
	}
	return listing, nil
}

// fetchGiteaRunes lists a folder and its immediate subfolders and parses every
// YAML in them — the same shape as the GitHub listing, so the browser and the
// update check do not care which forge a repository lives on.
func fetchGiteaRunes(ctx context.Context, host, repo, path, ref, token string) ([]ghRune, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	entries, err := giteaListDir(ctx, host, repo, path, ref, token)
	if err != nil {
		return nil, err
	}
	var candidates []ghRune
	add := func(e giteaEntry) {
		candidates = append(candidates, ghRune{Filename: e.Name, DownloadURL: giteaRawURL(host, repo, e.Path, ref)})
	}
	for _, e := range entries {
		switch {
		case e.Type == "file" && isYAMLName(e.Name):
			add(e)
		case e.Type == "dir":
			sub, serr := giteaListDir(ctx, host, repo, e.Path, ref, token)
			if serr != nil {
				continue // best-effort, as on GitHub
			}
			for _, se := range sub {
				if se.Type == "file" && isYAMLName(se.Name) {
					add(se)
				}
			}
		}
	}

	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i := range candidates {
		wg.Add(1)
		go func(g *ghRune) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := fetchGiteaRaw(ctx, host, g.DownloadURL, token)
			if err != nil {
				g.ParseError = err.Error()
				return
			}
			gs, err := gameskill.Parse(data)
			if err != nil {
				g.ParseError = err.Error()
				return
			}
			g.ID, g.Name, g.Category, g.Description, g.Version = gs.ID, gs.Name, gs.Category, gs.Description, gs.Version
		}(&candidates[i])
	}
	wg.Wait()
	sort.Slice(candidates, func(i, j int) bool {
		ni, nj := candidates[i].Name, candidates[j].Name
		if ni == "" {
			ni = candidates[i].Filename
		}
		if nj == "" {
			nj = candidates[j].Filename
		}
		return ni < nj
	})
	return candidates, nil
}

func fetchGiteaRaw(ctx context.Context, host, rawurl, token string) ([]byte, error) {
	resp, err := giteaHTTP(ctx, host, rawurl, token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && token == "" {
		return nil, fmt.Errorf("fetch returned 404 — if the repository is private, save a token for it")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch returned %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512*1024))
}

// fetchRunes lists a repository's runes on whichever forge it lives on.
func fetchRunes(ctx context.Context, host, repo, path, ref, token string) ([]ghRune, error) {
	if host == "" {
		return fetchGithubRunes(ctx, repo, path, ref, token)
	}
	return fetchGiteaRunes(ctx, host, repo, path, ref, token)
}
